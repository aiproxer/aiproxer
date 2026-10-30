package oci_test

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

	"github.com/aiproxer/aiproxer/connectors/oci/internal/service"
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

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation, userMsg string, nonStreaming bool, maxTokens *uint32) *memStream {
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
		Options: backendplugin.GenerationOptions{
			MaxOutputTokens:    maxTokens,
			ResponseSchemaJSON: backendplugin.RawJSONAbsentValue(),
		},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
}

func testKeyAndSecrets() (backendplugin.SecretBundle, string, string, string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	tenancy := "ocid1.tenancy.oc1..testtenancy"
	user := "ocid1.user.oc1..testuser"
	fingerprint := "20:3b:97:13:55:1c:5b:0d:d3:37:d8:50:4e:c9:42:ff"
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"private_key": pemBytes,
		},
	}
	return secrets, tenancy, user, fingerprint
}

// 1. Missing region/compartment fail closed.
func TestConfigure_RequiredFields_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	secrets, _, _, _ := testKeyAndSecrets()

	cases := []struct {
		name    string
		cfgYAML string
		errSub  string
	}{
		{
			name:    "missing region",
			cfgYAML: "compartment_id: ocid1.compartment.oc1..test\n",
			errSub:  "region is required",
		},
		{
			name:    "missing compartment_id",
			cfgYAML: "region: us-chicago-1\n",
			errSub:  "compartment_id is required",
		},
		{
			name:    "unexpected factory kind",
			cfgYAML: "region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\n",
			errSub:  "unexpected factory kind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind := service.FactoryKind
			if tc.name == "unexpected factory kind" {
				kind = "wrong-kind"
			}
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: kind,
				InstanceID:  "inst-1",
				ConfigYAML:  []byte(tc.cfgYAML),
				Secrets:     secrets,
			})
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

// 2. Constructed chat URL uses region host + /20231130/actions/chat.
// 3. api_origin keeps that path suffix.
func TestConfig_ChatEndpoint_URLConstruction(t *testing.T) {
	t.Parallel()

	// Default regional URL
	cfg1 := service.Config{Region: "us-chicago-1", CompartmentID: "comp-1"}
	want1 := "https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/20231130/actions/chat"
	if got := cfg1.ChatEndpoint(); got != want1 {
		t.Fatalf("ChatEndpoint() = %q, want %q", got, want1)
	}

	// api_origin override
	cfg2 := service.Config{Region: "us-chicago-1", CompartmentID: "comp-1", APIOrigin: "http://127.0.0.1:8080"}
	want2 := "http://127.0.0.1:8080/20231130/actions/chat"
	if got := cfg2.ChatEndpoint(); got != want2 {
		t.Fatalf("ChatEndpoint() = %q, want %q", got, want2)
	}

	// api_origin override with trailing slash
	cfg3 := service.Config{Region: "us-chicago-1", CompartmentID: "comp-1", APIOrigin: "http://127.0.0.1:8080/"}
	want3 := "http://127.0.0.1:8080/20231130/actions/chat"
	if got := cfg3.ChatEndpoint(); got != want3 {
		t.Fatalf("ChatEndpoint() = %q, want %q", got, want3)
	}

	// Management endpoint when api_origin is unset
	wantMgmt1 := "https://generativeai.us-chicago-1.oci.oraclecloud.com/20231130/models?compartmentId=comp-1"
	if got := cfg1.ManagementEndpoint(); got != wantMgmt1 {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, wantMgmt1)
	}

	// Management endpoint when api_origin is set
	wantMgmt2 := "http://127.0.0.1:8080/20231130/models?compartmentId=comp-1"
	if got := cfg2.ManagementEndpoint(); got != wantMgmt2 {
		t.Fatalf("ManagementEndpoint() = %q, want %q", got, wantMgmt2)
	}
}

// 6. YAML private_key rejected.
func TestConfigure_YAMLSecrets_Forbidden(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	secrets, _, _, _ := testKeyAndSecrets()

	secretKeys := []string{
		"private_key: PEM",
		"private_key_pem: PEM",
		"private_key_passphrase: PASS",
		"passphrase: PASS",
		"token: TOK",
		"api_key: KEY",
		"secret: SEC",
		"key: KEY",
	}

	for _, sk := range secretKeys {
		t.Run(sk, func(t *testing.T) {
			t.Parallel()
			cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\n%s\n", sk)
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-1",
				ConfigYAML:  []byte(cfgYAML),
				Secrets:     secrets,
			})
			if err == nil || !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
				t.Fatalf("expected literal secrets forbidden error for %q, got: %v", sk, err)
			}
		})
	}
}

// Defect 2: If private_key is in Secrets, require tenancy_ocid, user_ocid, and fingerprint from config or secrets. Missing any of them fails Configure. No dummy OCIDs.
func TestConfigure_PrivateKeySupplied_MissingTenancyUserFingerprint_FailsClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	secrets, tenancy, user, fp := testKeyAndSecrets()

	cases := []struct {
		name    string
		cfgYAML string
		errSub  string
	}{
		{
			name:    "missing tenancy_ocid",
			cfgYAML: fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\nuser_ocid: %s\nfingerprint: %s\n", user, fp),
			errSub:  "tenancy_ocid is required when private_key is supplied",
		},
		{
			name:    "missing user_ocid",
			cfgYAML: fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nfingerprint: %s\n", tenancy, fp),
			errSub:  "user_ocid is required when private_key is supplied",
		},
		{
			name:    "missing fingerprint",
			cfgYAML: fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\n", tenancy, user),
			errSub:  "fingerprint is required when private_key is supplied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-1",
				ConfigYAML:  []byte(tc.cfgYAML),
				Secrets:     secrets,
			})
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

// 8. Describe kind oci-generative-ai.
func TestDescribe_Metadata(t *testing.T) {
	t.Parallel()
	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	if desc.PluginID != service.PluginID {
		t.Fatalf("PluginID=%q want %q", desc.PluginID, service.PluginID)
	}
	if desc.ProtocolMajor != 1 || desc.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("unexpected protocol version %d.%d", desc.ProtocolMajor, desc.ProtocolMinor)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	f := desc.Factories[0]
	if f.Kind != service.FactoryKind {
		t.Fatalf("Factory kind=%q want %q", f.Kind, service.FactoryKind)
	}
	if !f.StaticCapabilities.Streaming {
		t.Fatalf("Streaming capability must be true")
	}
	if f.StaticCapabilities.Tools || f.StaticCapabilities.Vision {
		t.Fatalf("Tools and Vision capabilities must be false")
	}
	if !f.TransportCapabilities.Cancellation {
		t.Fatalf("Cancellation transport capability must be true")
	}
}

// 9. New() without signer/factory fails with NewProduction guidance.
func TestNew_WithoutConfig_FailsWithGuidance(t *testing.T) {
	t.Parallel()
	svc := service.New()

	cfgYAML := "region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\n"
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err == nil || !strings.Contains(err.Error(), "NewProduction") {
		t.Fatalf("expected NewProduction guidance error, got: %v", err)
	}

	prodSvc := service.NewProduction()
	if prodSvc == nil {
		t.Fatalf("NewProduction must return non-nil service")
	}
}

// 4. Execute sends GENERIC chat body with compartmentId and modelId; maps response text; Signature header present.
func TestConfiguredInstance_Execute_Chat_Generic_NonStreaming(t *testing.T) {
	t.Parallel()

	var chatCalled atomic.Bool
	var authHeader atomic.Pointer[string]
	var receivedBody atomic.Pointer[[]byte]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/20231130/actions/chat" {
			chatCalled.Store(true)
			auth := r.Header.Get("Authorization")
			authHeader.Store(&auth)

			body, _ := io.ReadAll(r.Body)
			receivedBody.Store(&body)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"chatResponse": {
					"apiFormat": "GENERIC",
					"choices": [
						{
							"index": 0,
							"message": {
								"role": "ASSISTANT",
								"content": [
									{"type": "TEXT", "text": "Hello from OCI Generative AI!"}
								]
							},
							"finishReason": "STOP"
						}
					]
				}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: meta.llama-3-70b-instruct\n", tenancy, user, fp, srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	maxTok := uint32(50)
	stream := newTestExecuteStream(context.Background(), "oci-generative-ai/meta.llama-3-70b-instruct", aipapi.OperationOpenAIChatCompletions, "Hello OCI", true, &maxTok)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !chatCalled.Load() {
		t.Fatalf("expected /20231130/actions/chat to be called")
	}

	auth := authHeader.Load()
	if auth == nil || !strings.HasPrefix(*auth, "Signature ") {
		t.Fatalf("expected OCI Signature header starting with 'Signature ', got %v", auth)
	}
	if strings.Contains(*auth, "Bearer") {
		t.Fatalf("Authorization header must NOT contain Bearer, got %s", *auth)
	}

	rawBody := receivedBody.Load()
	if rawBody == nil {
		t.Fatalf("expected request body to be recorded")
	}

	var reqBody struct {
		CompartmentID string `json:"compartmentId"`
		ServingMode   struct {
			ServingType string `json:"servingType"`
			ModelID     string `json:"modelId"`
		} `json:"servingMode"`
		ChatRequest struct {
			APIFormat string `json:"apiFormat"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
			MaxTokens *uint32 `json:"maxTokens"`
		} `json:"chatRequest"`
	}
	if err := json.Unmarshal(*rawBody, &reqBody); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	if reqBody.CompartmentID != "ocid1.compartment.oc1..test" {
		t.Fatalf("compartmentId=%q want %q", reqBody.CompartmentID, "ocid1.compartment.oc1..test")
	}
	if reqBody.ServingMode.ServingType != "ON_DEMAND" || reqBody.ServingMode.ModelID != "meta.llama-3-70b-instruct" {
		t.Fatalf("servingMode=%+v want ON_DEMAND meta.llama-3-70b-instruct", reqBody.ServingMode)
	}
	if reqBody.ChatRequest.APIFormat != "GENERIC" {
		t.Fatalf("apiFormat=%q want GENERIC", reqBody.ChatRequest.APIFormat)
	}
	if len(reqBody.ChatRequest.Messages) != 1 || reqBody.ChatRequest.Messages[0].Content[0].Text != "Hello OCI" {
		t.Fatalf("unexpected messages: %+v", reqBody.ChatRequest.Messages)
	}
	if reqBody.ChatRequest.MaxTokens == nil || *reqBody.ChatRequest.MaxTokens != 50 {
		t.Fatalf("expected maxTokens=50, got %v", reqBody.ChatRequest.MaxTokens)
	}

	var textDeltas []string
	var terminalSeen bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
			textDeltas = append(textDeltas, *f.Event.Delta)
		}
		if f.Kind == backendplugin.ServerFrameTerminal {
			terminalSeen = true
			if f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal status=%v want Success", f.Terminal.Status)
			}
		}
	}

	if len(textDeltas) != 1 || textDeltas[0] != "Hello from OCI Generative AI!" {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
	if !terminalSeen {
		t.Fatalf("expected terminal frame")
	}
}

// 5. Hard-negative: /20231130/actions/chat 404 + /openai/v1/chat/completions 200 on same httptest -> Execute fails; openai path unused.
func TestConfiguredInstance_Execute_HardNegative404(t *testing.T) {
	t.Parallel()

	var openaiCalled atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/openai/v1/chat/completions") {
			openaiCalled.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "wrong path"}}]}`))
			return
		}
		if strings.Contains(r.URL.Path, "/actions/chat") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: meta.llama-3-70b-instruct\n", tenancy, user, fp, srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "oci-generative-ai/meta.llama-3-70b-instruct", aipapi.OperationOpenAIChatCompletions, "test", true, nil)
	execErr := inst.Execute(stream)
	if execErr == nil {
		var hasTerminalError bool
		for _, f := range stream.outbox {
			if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalFailure {
				hasTerminalError = true
			}
		}
		if !hasTerminalError {
			t.Fatalf("expected Execute or stream terminal to fail on 404")
		}
	}

	if count := openaiCalled.Load(); count != 0 {
		t.Fatalf("hard negative violation: /openai/v1/chat/completions was called %d times; OCI connector must never fallback to OpenAI", count)
	}
}

// Streaming chat action returns SSE text/event-stream.
func TestConfiguredInstance_Execute_Chat_Generic_Streaming(t *testing.T) {
	t.Parallel()

	var streamCalled atomic.Bool
	var authHeader atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/20231130/actions/chat" {
			streamCalled.Store(true)
			auth := r.Header.Get("Authorization")
			authHeader.Store(&auth)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}

			chunk1 := `data: {"chatResponse": {"apiFormat": "GENERIC", "choices": [{"index": 0, "message": {"role": "ASSISTANT", "content": [{"type": "TEXT", "text": "Hello "}]}}]}}` + "\n\n"
			_, _ = w.Write([]byte(chunk1))
			if ok {
				flusher.Flush()
			}

			chunk2 := `data: {"chatResponse": {"apiFormat": "GENERIC", "choices": [{"index": 0, "message": {"role": "ASSISTANT", "content": [{"type": "TEXT", "text": "streaming OCI!"}]}}]}}` + "\n\n"
			_, _ = w.Write([]byte(chunk2))
			if ok {
				flusher.Flush()
			}

			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if ok {
				flusher.Flush()
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: meta.llama-3-70b-instruct\n", tenancy, user, fp, srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "oci-generative-ai/meta.llama-3-70b-instruct", aipapi.OperationOpenAIChatCompletions, "Hello OCI stream", false, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !streamCalled.Load() {
		t.Fatalf("expected /20231130/actions/chat to be called")
	}

	auth := authHeader.Load()
	if auth == nil || !strings.HasPrefix(*auth, "Signature ") {
		t.Fatalf("expected Signature header, got %v", auth)
	}

	var textDeltas []string
	var terminalSeen bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
			textDeltas = append(textDeltas, *f.Event.Delta)
		}
		if f.Kind == backendplugin.ServerFrameTerminal {
			terminalSeen = true
			if f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal status=%v want Success", f.Terminal.Status)
			}
		}
	}

	if len(textDeltas) != 2 || textDeltas[0] != "Hello " || textDeltas[1] != "streaming OCI!" {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
	if !terminalSeen {
		t.Fatalf("expected terminal frame")
	}
}

// ListModels queries /20231130/models with compartmentId query and drops embed/rerank/image by heuristic.
func TestConfiguredInstance_ListModels(t *testing.T) {
	t.Parallel()

	var listCalled atomic.Bool
	var capturedCompartment atomic.Pointer[string]
	var authHeader atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/20231130/models" {
			listCalled.Store(true)
			comp := r.URL.Query().Get("compartmentId")
			capturedCompartment.Store(&comp)
			auth := r.Header.Get("Authorization")
			authHeader.Store(&auth)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"modelCollection": {
					"items": [
						{"id": "ocid1.model.oc1..llama3", "displayName": "meta.llama-3-70b-instruct"},
						{"id": "ocid1.model.oc1..cohere", "displayName": "cohere.command-r-16k"},
						{"id": "ocid1.model.oc1..embed", "displayName": "cohere.embed-english-v3.0"},
						{"id": "ocid1.model.oc1..rerank", "displayName": "cohere.rerank-v3.5"},
						{"id": "ocid1.model.oc1..img", "displayName": "stability.sd-image"}
					]
				}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\n", tenancy, user, fp, srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	if !listCalled.Load() {
		t.Fatalf("expected /20231130/models to be called")
	}

	comp := capturedCompartment.Load()
	if comp == nil || *comp != "ocid1.compartment.oc1..test" {
		t.Fatalf("expected compartmentId=ocid1.compartment.oc1..test, got %v", comp)
	}

	auth := authHeader.Load()
	if auth == nil || !strings.HasPrefix(*auth, "Signature ") {
		t.Fatalf("expected Signature header, got %v", auth)
	}

	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 non-embed models, got %d: %+v", len(resp.Models), resp.Models)
	}

	names := []string{resp.Models[0].CanonicalModelID, resp.Models[1].CanonicalModelID}
	if names[0] != "oci-generative-ai/meta.llama-3-70b-instruct" || names[1] != "oci-generative-ai/cohere.command-r-16k" {
		t.Fatalf("unexpected models: %v", names)
	}
}

// Defect 1: Management GET 404 -> ListModels returns error (fails closed, no singleModelResponse fallback).
func TestConfiguredInstance_ListModels_404_FailsClosed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	secrets, tenancy, user, fp := testKeyAndSecrets()
	svc := service.New(service.WithRequestSignerFactory(service.DefaultRequestSignerFactory))
	// Even with model_id configured, a 404 from the management API must fail closed
	cfgYAML := fmt.Sprintf("region: us-chicago-1\ncompartment_id: ocid1.compartment.oc1..test\ntenancy_ocid: %s\nuser_ocid: %s\nfingerprint: %s\napi_origin: %s\nmodel_id: meta.llama-3-70b-instruct\n", tenancy, user, fp, srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	_, err = inst.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatalf("expected ListModels to fail closed on management 404, got nil error")
	}
	if !strings.Contains(err.Error(), "status 404") && !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected error containing 404, got: %v", err)
	}
}
