package sagemaker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/sagemaker/internal/service"
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

func testStaticSecrets() backendplugin.SecretBundle {
	return backendplugin.SecretBundle{
		Values: map[string][]byte{
			"aws_access_key_id":     []byte("AKIAIOSFODNN7EXAMPLE"),
			"aws_secret_access_key": []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"),
		},
	}
}

// 1. Missing region / endpoint_name / inference_contract fail closed.
func TestConfigure_RequiredFields_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))

	cases := []struct {
		name    string
		cfgYAML string
		errSub  string
	}{
		{
			name:    "missing region",
			cfgYAML: "endpoint_name: my-ep\ninference_contract: hf-text-generation\n",
			errSub:  "region is required",
		},
		{
			name:    "missing endpoint_name",
			cfgYAML: "region: us-east-1\ninference_contract: hf-text-generation\n",
			errSub:  "endpoint_name is required",
		},
		{
			name:    "missing inference_contract",
			cfgYAML: "region: us-east-1\nendpoint_name: my-ep\n",
			errSub:  "inference_contract is required",
		},
		{
			name:    "unexpected factory kind",
			cfgYAML: "region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\n",
			errSub:  "unexpected factory kind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind := service.FactoryKind
			if tc.name == "unexpected factory kind" {
				kind = "other-kind"
			}
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: kind,
				InstanceID:  "inst-1",
				ConfigYAML:  []byte(tc.cfgYAML),
				Secrets:     testStaticSecrets(),
			})
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

// 2. Unknown inference_contract fail closed.
func TestConfigure_UnknownInferenceContract_FailsClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))

	cfgYAML := "region: us-east-1\nendpoint_name: my-ep\ninference_contract: openai-chat\n"
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported inference_contract") {
		t.Fatalf("expected unsupported inference_contract error, got: %v", err)
	}
}

// 3. YAML AWS keys rejected.
func TestConfigure_YAMLSecrets_Forbidden(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))

	secretKeys := []string{
		"aws_access_key_id: AKID",
		"aws_secret_access_key: SECRET",
		"aws_session_token: TOKEN",
		"access_key: AKID",
		"secret_key: SECRET",
		"api_key: KEY",
		"token: TOK",
		"secret: SEC",
	}

	for _, sk := range secretKeys {
		t.Run(sk, func(t *testing.T) {
			t.Parallel()
			cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\n%s\n", sk)
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst-1",
				ConfigYAML:  []byte(cfgYAML),
				Secrets:     testStaticSecrets(),
			})
			if err == nil || !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
				t.Fatalf("expected literal secrets forbidden error for %q, got: %v", sk, err)
			}
		})
	}
}

// 9. Describe factory kind sagemaker. Capabilities: Streaming true (unary InvokeEndpoint emits canonical events); Tools/Vision false.
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
		t.Fatalf("Streaming capability must be true: InvokeEndpoint emits canonical events over a managed stream")
	}
	if f.StaticCapabilities.Tools || f.StaticCapabilities.Vision {
		t.Fatalf("Tools and Vision capabilities must be false")
	}
	if !f.TransportCapabilities.Cancellation {
		t.Fatalf("Cancellation transport capability must be true")
	}
}

// 10. New() without AWS config fails Configure with NewProduction guidance; NewProduction is wired in main.
func TestNew_WithoutConfig_FailsWithGuidance(t *testing.T) {
	t.Parallel()
	svc := service.New() // no client factory or static clients

	cfgYAML := "region: us-east-1\nendpoint_name: my-ep\ninference_contract: hf-text-generation\n"
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

// 4. Configure+Execute hf-text-generation: body has inputs, response generated_text becomes a text delta.
// Path/host is the SageMaker runtime invoke, not OpenAI chat/completions.
// SigV4 Authorization header is verified.
func TestConfiguredInstance_Execute_HFTextGen_NonStreaming(t *testing.T) {
	t.Parallel()

	var invokeCalled atomic.Bool
	var authHeader atomic.Pointer[string]
	var receivedBody atomic.Pointer[[]byte]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/endpoints/test-ep/invocations" {
			invokeCalled.Store(true)
			auth := r.Header.Get("Authorization")
			authHeader.Store(&auth)

			body, _ := io.ReadAll(r.Body)
			receivedBody.Store(&body)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generated_text": "Hello from SageMaker non-streaming!"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: test-ep\ninference_contract: hf-text-generation\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	maxTok := uint32(42)
	stream := newTestExecuteStream(context.Background(), "sagemaker/test-ep", aipapi.OperationOpenAIChatCompletions, "Hello world", true, &maxTok)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !invokeCalled.Load() {
		t.Fatalf("expected /endpoints/test-ep/invocations to be called")
	}

	auth := authHeader.Load()
	if auth == nil || !strings.HasPrefix(*auth, "AWS4-HMAC-SHA256") {
		t.Fatalf("expected SigV4 Authorization header starting with AWS4-HMAC-SHA256, got %v", auth)
	}

	body := receivedBody.Load()
	if body == nil {
		t.Fatalf("expected request body to be recorded")
	}
	var reqBody service.HFTextGenRequest
	if err := json.Unmarshal(*body, &reqBody); err != nil {
		t.Fatalf("failed to unmarshal request body: %v", err)
	}
	if reqBody.Inputs != "Hello world" {
		t.Fatalf("reqBody.Inputs=%q want %q", reqBody.Inputs, "Hello world")
	}
	if reqBody.Parameters == nil || reqBody.Parameters.MaxNewTokens == nil || *reqBody.Parameters.MaxNewTokens != 42 {
		t.Fatalf("expected max_new_tokens=42, got %+v", reqBody.Parameters)
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

	if len(textDeltas) != 1 || textDeltas[0] != "Hello from SageMaker non-streaming!" {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
	if !terminalSeen {
		t.Fatalf("expected terminal frame")
	}
}

// 5. Hard-negative: InvokeEndpoint 404 while a /chat/completions 200 exists on the same httptest -> Execute fails; chat path unused.
func TestConfiguredInstance_Execute_HardNegative404(t *testing.T) {
	t.Parallel()

	var chatCompletionsCalled atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" || strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCompletionsCalled.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "wrong path"}}]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/endpoints/") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: not-found-ep\ninference_contract: hf-text-generation\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sagemaker/not-found-ep", aipapi.OperationOpenAIChatCompletions, "test", true, nil)
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

	if count := chatCompletionsCalled.Load(); count != 0 {
		t.Fatalf("hard negative violation: /chat/completions was called %d times; SageMaker connector must never fallback to OpenAI", count)
	}
}

// 6. Streaming delivery is served via unary InvokeEndpoint collect (no provider
// event-stream buffering): a streaming request must NOT touch
// invocations-response-stream and still yields the generated_text delta.
func TestConfiguredInstance_Execute_HFTextGen_Streaming(t *testing.T) {
	t.Parallel()

	var unaryInvokeCalled atomic.Bool
	var streamInvokeCalled atomic.Bool
	var authHeader atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "invocations-response-stream") {
			streamInvokeCalled.Store(true)
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/endpoints/stream-ep/invocations" {
			unaryInvokeCalled.Store(true)
			auth := r.Header.Get("Authorization")
			authHeader.Store(&auth)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generated_text": "Streamed text from SageMaker!"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: stream-ep\ninference_contract: hf-text-generation\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sagemaker/stream-ep", aipapi.OperationOpenAIChatCompletions, "Stream test", false, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !unaryInvokeCalled.Load() {
		t.Fatalf("expected /endpoints/stream-ep/invocations to be called for streaming delivery (unary collect)")
	}
	if streamInvokeCalled.Load() {
		t.Fatalf("provider event stream must not be used: invocations-response-stream was called")
	}

	auth := authHeader.Load()
	if auth == nil || !strings.HasPrefix(*auth, "AWS4-HMAC-SHA256") {
		t.Fatalf("expected SigV4 Authorization header starting with AWS4-HMAC-SHA256, got %v", auth)
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

	if len(textDeltas) != 1 || textDeltas[0] != "Streamed text from SageMaker!" {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
	if !terminalSeen {
		t.Fatalf("expected terminal frame")
	}
}

// 7. ListModels exposes only the configured endpoint (no control-plane
// enumeration); Execute of a non-configured endpoint fails.
// The control-plane stub below advertises extra endpoints to prove they are
// neither exposed nor consulted.
func TestConfiguredInstance_ListModels_And_UnconfiguredEndpointFails(t *testing.T) {
	t.Parallel()

	var listEndpointsCalled atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Header.Get("X-Amz-Target") == "SageMaker.ListEndpoints" {
			listEndpointsCalled.Add(1)
			w.Header().Set("Content-Type", "application/x-amz-json-1.1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"Endpoints": [
					{"EndpointName": "configured-ep", "EndpointStatus": "InService"},
					{"EndpointName": "other-ep", "EndpointStatus": "InService"},
					{"EndpointName": "failed-ep", "EndpointStatus": "Failed"}
				]
			}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/endpoints/configured-ep/invocations" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"generated_text": "configured ep response"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: configured-ep\ninference_contract: hf-text-generation\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	listResp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	if len(listResp.Models) != 1 {
		t.Fatalf("expected exactly 1 configured model, got %d: %+v", len(listResp.Models), listResp.Models)
	}
	if listResp.Models[0].CanonicalModelID != "sagemaker/configured-ep" {
		t.Fatalf("expected sagemaker/configured-ep, got %v", listResp.Models[0].CanonicalModelID)
	}
	if !listResp.Models[0].Capabilities.Streaming {
		t.Fatalf("provider streaming must be advertised")
	}
	if count := listEndpointsCalled.Load(); count != 0 {
		t.Fatalf("control plane ListEndpoints was called %d times; inventory must expose only the configured endpoint", count)
	}

	// Executing configured endpoint succeeds
	streamOK := newTestExecuteStream(context.Background(), "sagemaker/configured-ep", aipapi.OperationOpenAIChatCompletions, "test", true, nil)
	if err := inst.Execute(streamOK); err != nil {
		t.Fatalf("Execute configured-ep failed: %v", err)
	}
	var sawOK bool
	for _, f := range streamOK.outbox {
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal.Status == backendplugin.TerminalSuccess {
			sawOK = true
		}
	}
	if !sawOK {
		t.Fatalf("expected terminal OK for configured endpoint")
	}

	// Executing other listed-but-not-configured endpoint fails closed
	streamOther := newTestExecuteStream(context.Background(), "sagemaker/other-ep", aipapi.OperationOpenAIChatCompletions, "test", true, nil)
	errOther := inst.Execute(streamOther)
	var sawOtherErr bool
	for _, f := range streamOther.outbox {
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal.Status == backendplugin.TerminalFailure {
			sawOtherErr = true
		}
	}
	if errOther == nil && !sawOtherErr {
		t.Fatalf("expected execute of listed-but-unconfigured endpoint 'other-ep' to fail closed")
	}
	if errOther != nil && !strings.Contains(errOther.Error(), "does not match configured endpoint_name") {
		t.Fatalf("expected explicit endpoint_name mismatch error, got: %v", errOther)
	}
	if count := listEndpointsCalled.Load(); count != 0 {
		t.Fatalf("control plane ListEndpoints was called %d times; it must never be consulted", count)
	}
}

// Non-user roles or tools fail closed rather than silent drop.
func TestConfiguredInstance_Execute_UnsupportedSemantics_FailsClosed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithAWSClientFactory(service.DefaultAWSClientFactory))
	cfgYAML := fmt.Sprintf("region: us-east-1\nendpoint_name: test-ep\ninference_contract: hf-text-generation\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     testStaticSecrets(),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// Assistant message role
	msg := "Hello"
	invAssistant := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "al-1",
		BLegID:           "bl-1",
		CanonicalModelID: "sagemaker/test-ep",
		Operation:        string(aipapi.OperationOpenAIChatCompletions),
		DeliveryMode:     string(aipapi.DeliveryModeNonStreaming),
		TransportMode:    string(aipapi.TransportModeNonStreaming),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleAssistant,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
	}
	stream := &memStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "inst-1", Invocation: &invAssistant},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "inst-1"},
		},
	}
	err = inst.Execute(stream)
	var sawErr bool
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal.Status == backendplugin.TerminalFailure {
			sawErr = true
		}
	}
	if err == nil && !sawErr {
		t.Fatalf("expected non-user message to fail closed")
	}
}
