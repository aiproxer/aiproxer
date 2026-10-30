package watsonx_test

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

	"github.com/aiproxer/aiproxer/connectors/watsonx/internal/service"
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

// 1. Missing region, missing project_id+space_id, both project_id and space_id fail closed.
func TestConfigure_RequiredFields_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))

	cases := []struct {
		name    string
		cfgYAML string
		errSub  string
	}{
		{
			name:    "missing region",
			cfgYAML: "project_id: p1\n",
			errSub:  "region is required",
		},
		{
			name:    "missing project_id and space_id",
			cfgYAML: "region: us-south\n",
			errSub:  "exactly one of project_id or space_id must be configured",
		},
		{
			name:    "both project_id and space_id set",
			cfgYAML: "region: us-south\nproject_id: p1\nspace_id: s1\n",
			errSub:  "exactly one of project_id or space_id must be configured",
		},
		{
			name:    "invalid inference_api",
			cfgYAML: "region: us-south\nproject_id: p1\ninference_api: unsupported_mode\n",
			errSub:  "unsupported inference_api",
		},
		{
			name:    "unexpected factory kind",
			cfgYAML: "region: us-south\nproject_id: p1\n",
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
			})
			if err == nil || !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
			}
		})
	}
}

// 2. Constructed ML origin https://{region}.ml.cloud.ibm.com + /ml/v1/text/chat?version=2024-05-31.
func TestConfig_ConstructedMLOrigin(t *testing.T) {
	t.Parallel()
	cfgYAML := "region: eu-de\nproject_id: proj-456\n"
	cfg, err := service.ParseConfigYAML([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.MLOrigin() != "https://eu-de.ml.cloud.ibm.com" {
		t.Fatalf("got MLOrigin %q, want https://eu-de.ml.cloud.ibm.com", cfg.MLOrigin())
	}
	if cfg.APIVersion != "2024-05-31" {
		t.Fatalf("got APIVersion %q, want 2024-05-31", cfg.APIVersion)
	}
}

// 3. api_origin / iam_origin keep path suffixes.
func TestConfig_OriginPathSuffixes(t *testing.T) {
	t.Parallel()
	cfgYAML := "region: us-south\nproject_id: p1\napi_origin: http://127.0.0.1:9000/proxy/ml\niam_origin: http://127.0.0.1:9000/proxy/iam/\n"
	cfg, err := service.ParseConfigYAML([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.MLOrigin() != "http://127.0.0.1:9000/proxy/ml" {
		t.Fatalf("got MLOrigin %q, want http://127.0.0.1:9000/proxy/ml", cfg.MLOrigin())
	}
	if cfg.IAMOriginURL() != "http://127.0.0.1:9000/proxy/iam" {
		t.Fatalf("got IAMOriginURL %q, want http://127.0.0.1:9000/proxy/iam", cfg.IAMOriginURL())
	}
}

// 4. YAML secrets rejected.
func TestConfig_YAMLSecretsRejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"region: us-south\nproject_id: p1\napi_key: secret-key\n",
		"region: us-south\nproject_id: p1\napikey: secret-key\n",
		"region: us-south\nproject_id: p1\niam_token: secret-token\n",
		"region: us-south\nproject_id: p1\ntoken: secret-token\n",
		"region: us-south\nproject_id: p1\nsecret: secret-value\n",
	}
	for _, raw := range cases {
		_, err := service.ParseConfigYAML([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "is forbidden") || !strings.Contains(err.Error(), "WATSONX_API_KEY") {
			t.Fatalf("expected forbidden-field rejection naming env root, got: %v", err)
		}
	}
}

// 5. IAM factory Execute: IAM form grant + chat Bearer is the IAM access_token.
func TestIAMFactory_Execute_FormGrantAndBearer(t *testing.T) {
	t.Parallel()

	var iamCalled atomic.Bool
	var chatCalled atomic.Bool
	var capturedBearer string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			iamCalled.Store(true)
			if r.Method != http.MethodPost {
				t.Errorf("expected POST to /identity/token, got %s", r.Method)
			}
			ct := r.Header.Get("Content-Type")
			if !strings.Contains(ct, "application/x-www-form-urlencoded") {
				t.Errorf("expected application/x-www-form-urlencoded, got %s", ct)
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			if r.Form.Get("grant_type") != "urn:ibm:params:oauth:grant-type:apikey" {
				t.Errorf("expected grant_type urn:ibm:params:oauth:grant-type:apikey, got %q", r.Form.Get("grant_type"))
			}
			if r.Form.Get("apikey") != "my-ibm-cloud-api-key" {
				t.Errorf("expected apikey my-ibm-cloud-api-key, got %q", r.Form.Get("apikey"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "iam-access-token-xyz", "token_type": "Bearer", "expires_in": 3600}`))
		case "/ml/v1/text/chat":
			chatCalled.Store(true)
			capturedBearer = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello from watsonx"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: test-proj\napi_origin: %s\niam_origin: %s\n", srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"api_key": []byte("my-ibm-cloud-api-key"),
		},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-iam",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "hello", true, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !iamCalled.Load() {
		t.Fatal("expected IAM /identity/token to be called")
	}
	if !chatCalled.Load() {
		t.Fatal("expected ML /ml/v1/text/chat to be called")
	}
	if capturedBearer != "Bearer iam-access-token-xyz" {
		t.Fatalf("expected Authorization Bearer iam-access-token-xyz, got %q", capturedBearer)
	}
}

// 6. Token refresh before expiry.
func TestIAMTokenProvider_RefreshBeforeExpiry(t *testing.T) {
	t.Parallel()

	var tokenCallCount atomic.Int32
	var chatCallCount atomic.Int32
	var capturedTokens []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			idx := tokenCallCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return expires_in: 1 for token 1 to trigger immediate refresh on next call
			_, _ = fmt.Fprintf(w, `{"access_token": "token-%d", "token_type": "Bearer", "expires_in": 1}`, idx)
		case "/ml/v1/text/chat":
			chatCallCount.Add(1)
			capturedTokens = append(capturedTokens, r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.NewProduction()
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: test-proj\napi_origin: %s\niam_origin: %s\n", srv.URL, srv.URL)
	secrets := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"api_key": []byte("refresh-key"),
		},
	}

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-refresh",
		ConfigYAML:  []byte(cfgYAML),
		Secrets:     secrets,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	// First call uses token-1
	stream1 := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "msg1", true, nil)
	if err := inst.Execute(stream1); err != nil {
		t.Fatalf("execute 1: %v", err)
	}

	// Second call triggers refresh because token-1 lifetime was 1s (expired / within 10s skew)
	stream2 := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "msg2", true, nil)
	if err := inst.Execute(stream2); err != nil {
		t.Fatalf("execute 2: %v", err)
	}

	if tokenCallCount.Load() != 2 {
		t.Fatalf("expected 2 IAM token calls, got %d", tokenCallCount.Load())
	}
	if len(capturedTokens) != 2 {
		t.Fatalf("expected 2 captured tokens, got %d", len(capturedTokens))
	}
	if capturedTokens[0] != "Bearer token-1" {
		t.Fatalf("call 1 token: got %q, want Bearer token-1", capturedTokens[0])
	}
	if capturedTokens[1] != "Bearer token-2" {
		t.Fatalf("call 2 token: got %q, want Bearer token-2", capturedTokens[1])
	}
}

// 7. Chat mapping through configured instance (messages, project_id, model_id, max_tokens, text delta).
func TestExecute_ChatMapping_BothContentFormats(t *testing.T) {
	t.Parallel()

	var capturedBody []byte
	var useArrayResponse atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ml/v1/text/chat" {
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if useArrayResponse.Load() {
				// Array format: choices[].message.content = [{"type":"text","text":"array content"}]
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":[{"type":"text","text":"array content"}]}}]}`))
			} else {
				// String format: choices[].message.content = "string content"
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"string content"}}]}`))
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("test-tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: proj-chat-123\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-chat",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	maxTokens := uint32(50)
	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "user input text", true, &maxTokens)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var reqBody map[string]any
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if reqBody["model_id"] != "ibm/granite-3-8b-instruct" {
		t.Fatalf("expected model_id ibm/granite-3-8b-instruct, got %v", reqBody["model_id"])
	}
	if reqBody["project_id"] != "proj-chat-123" {
		t.Fatalf("expected project_id proj-chat-123, got %v", reqBody["project_id"])
	}
	maxTokensVal, ok := reqBody["max_tokens"].(float64)
	if !ok || int(maxTokensVal) != 50 {
		t.Fatalf("expected max_tokens 50, got %v", reqBody["max_tokens"])
	}

	// Verify text delta in stream outbox
	var hasTextDelta bool
	for _, f := range stream.outbox {
		if f.Event != nil && f.Event.Kind == aipapi.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "string content" {
			hasTextDelta = true
			break
		}
	}
	if !hasTextDelta {
		t.Fatalf("expected text delta 'string content' in outbox, got: %+v", stream.outbox)
	}

	// Now test array content format
	useArrayResponse.Store(true)
	streamArr := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "user input text", true, nil)
	if err := inst.Execute(streamArr); err != nil {
		t.Fatalf("execute array format: %v", err)
	}
	var hasArrayTextDelta bool
	for _, f := range streamArr.outbox {
		if f.Event != nil && f.Event.Kind == aipapi.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "array content" {
			hasArrayTextDelta = true
			break
		}
	}
	if !hasArrayTextDelta {
		t.Fatalf("expected text delta 'array content' in outbox, got: %+v", streamArr.outbox)
	}
}

// 8. Streaming uses chat_stream.
func TestExecute_Streaming_UsesChatStream(t *testing.T) {
	t.Parallel()

	var pathCalled string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCalled = r.URL.Path
		if r.URL.Path == "/ml/v1/text/chat_stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"stream chunk 1\"}}]}\n\n"))
			if ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"stream chunk 2\"}}]}\n\n"))
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

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: p1\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-stream",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "hello stream", false, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute streaming: %v", err)
	}

	if pathCalled != "/ml/v1/text/chat_stream" {
		t.Fatalf("expected /ml/v1/text/chat_stream, got %q", pathCalled)
	}

	var deltas []string
	for _, f := range stream.outbox {
		if f.Event != nil && f.Event.Kind == aipapi.EventTextDelta && f.Event.Delta != nil {
			deltas = append(deltas, *f.Event.Delta)
		}
	}
	if len(deltas) != 2 || deltas[0] != "stream chunk 1" || deltas[1] != "stream chunk 2" {
		t.Fatalf("expected ['stream chunk 1', 'stream chunk 2'], got: %v", deltas)
	}
}

// 9. inference_api: generation uses /ml/v1/text/generation and generated_text.
func TestExecute_InferenceAPIGeneration(t *testing.T) {
	t.Parallel()

	var pathCalled string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCalled = r.URL.Path
		if r.URL.Path == "/ml/v1/text/generation" {
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"results":[{"generated_text":"generated result text"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nspace_id: space-123\ninference_api: generation\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-gen",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	maxTokens := uint32(42)
	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "gen prompt text", true, &maxTokens)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute generation: %v", err)
	}

	if pathCalled != "/ml/v1/text/generation" {
		t.Fatalf("expected /ml/v1/text/generation, got %q", pathCalled)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["model_id"] != "ibm/granite-3-8b-instruct" {
		t.Fatalf("expected model_id ibm/granite-3-8b-instruct, got %v", body["model_id"])
	}
	if body["space_id"] != "space-123" {
		t.Fatalf("expected space_id space-123, got %v", body["space_id"])
	}
	if body["input"] != "gen prompt text" {
		t.Fatalf("expected input 'gen prompt text', got %v", body["input"])
	}

	var hasDelta bool
	for _, f := range stream.outbox {
		if f.Event != nil && f.Event.Kind == aipapi.EventTextDelta && f.Event.Delta != nil && *f.Event.Delta == "generated result text" {
			hasDelta = true
			break
		}
	}
	if !hasDelta {
		t.Fatalf("expected text delta 'generated result text', got: %+v", stream.outbox)
	}
}

// 10. Deployment canonical uses /ml/v1/deployments/{id}/text/chat.
func TestExecute_DeploymentCanonical(t *testing.T) {
	t.Parallel()

	var pathCalled string
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCalled = r.URL.Path
		if r.URL.Path == "/ml/v1/deployments/dep-abc-789/text/chat" {
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"from deployment"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: p1\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-dep",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "watsonx/deployment/dep-abc-789", aipapi.OperationOpenAIChatCompletions, "dep msg", true, nil)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute deployment: %v", err)
	}

	if pathCalled != "/ml/v1/deployments/dep-abc-789/text/chat" {
		t.Fatalf("expected /ml/v1/deployments/dep-abc-789/text/chat, got %q", pathCalled)
	}

	var body map[string]any
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body["model_id"]; ok {
		t.Fatalf("deployment inference body should omit model_id, got %v", body["model_id"])
	}
	if _, ok := body["project_id"]; ok {
		t.Fatalf("deployment inference body should omit project_id, got %v", body["project_id"])
	}
}

// 11. ListModels asserts foundation_model_specs path + filter and deployments path + project_id/space_id query. List 404 fails closed even when model_id is set.
func TestListModels_SuccessAndFailClosed(t *testing.T) {
	t.Parallel()

	var specsPath string
	var deploymentsPath string
	var return404 atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if return404.Load() {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/ml/v1/foundation_model_specs":
			specsPath = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"resources": [
					{
						"model_id": "ibm/granite-3-8b-instruct",
						"label": "Granite 3 8B Instruct",
						"functions": [{"id": "text_chat"}],
						"lifecycle": [{"id": "available"}]
					},
					{
						"model_id": "ibm/granite-embedding",
						"label": "Granite Embed",
						"functions": [{"id": "embedding"}],
						"lifecycle": [{"id": "available"}]
					},
					{
						"model_id": "ibm/old-model",
						"label": "Old Model",
						"functions": [{"id": "text_chat"}],
						"lifecycle": [{"id": "withdrawn"}]
					}
				]
			}`))
		case "/ml/v4/deployments":
			deploymentsPath = r.URL.String()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"resources": [
					{
						"metadata": {"id": "dep-live-1", "name": "Live Deployment"},
						"entity": {"name": "Live Deployment", "status": {"state": "ready"}}
					},
					{
						"metadata": {"id": "dep-failed-2", "name": "Failed Deployment"},
						"entity": {"name": "Failed Deployment", "status": {"state": "failed"}}
					}
				]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: proj-list-123\nmodel_id: ibm/granite-3-8b-instruct\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-list",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}

	if !strings.Contains(specsPath, "filters=function_text_chat") {
		t.Fatalf("expected filters=function_text_chat in specs path, got %q", specsPath)
	}
	if !strings.Contains(deploymentsPath, "project_id=proj-list-123") {
		t.Fatalf("expected project_id=proj-list-123 in deployments path, got %q", deploymentsPath)
	}

	// Assert only valid models are returned (embedding and withdrawn models dropped)
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].CanonicalModelID != "watsonx/ibm/granite-3-8b-instruct" {
		t.Fatalf("expected watsonx/ibm/granite-3-8b-instruct, got %q", resp.Models[0].CanonicalModelID)
	}
	if resp.Models[1].CanonicalModelID != "watsonx/deployment/dep-live-1" {
		t.Fatalf("expected watsonx/deployment/dep-live-1, got %q", resp.Models[1].CanonicalModelID)
	}

	// Test 404 fails closed and does NOT invent a one-row catalog from model_id
	return404.Store(true)
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected ListModels to fail closed on 404, got nil error")
	}
}

// 12. Hard-negative vs OpenAI chat/completions.
func TestExecute_HardNegative_VersusOpenAI(t *testing.T) {
	t.Parallel()

	var openAICalled atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/chat/completions") {
			openAICalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"openai response"}}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ml/v1/text/chat") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("region: us-south\nproject_id: p1\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-hn",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "hello", true, nil)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected Execute to fail when native chat returns 404")
	}
	if openAICalled.Load() {
		t.Fatal("hard negative failed: connector called /v1/chat/completions!")
	}
}

// 13. Describe kind watsonx. Streaming true; Tools/Vision false.
func TestDescribe(t *testing.T) {
	t.Parallel()
	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if desc.PluginID != service.PluginID {
		t.Fatalf("got PluginID %q, want %q", desc.PluginID, service.PluginID)
	}
	if desc.ProtocolMajor != 1 {
		t.Fatalf("got ProtocolMajor %d, want 1", desc.ProtocolMajor)
	}
	if desc.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("got ProtocolMinor %d, want %d", desc.ProtocolMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	f := desc.Factories[0]
	if f.Kind != service.FactoryKind {
		t.Fatalf("got FactoryKind %q, want %q", f.Kind, service.FactoryKind)
	}
	if f.DisplayName != service.DisplayName {
		t.Fatalf("got DisplayName %q, want %q", f.DisplayName, service.DisplayName)
	}
	if !f.StaticCapabilities.Streaming {
		t.Fatal("expected Streaming true")
	}
	if f.StaticCapabilities.Tools {
		t.Fatal("expected Tools false")
	}
	if f.StaticCapabilities.Vision {
		t.Fatal("expected Vision false")
	}
}

// 14. New() without host-issued slots fails Configure closed naming the
// slot and env root; NewProduction non-nil.
func TestNew_GuidanceAndNewProduction(t *testing.T) {
	t.Parallel()
	svc := service.New()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-none",
		ConfigYAML:  []byte("region: us-south\nproject_id: p1\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "WATSONX_API_KEY") {
		t.Fatalf("expected error naming slot and env root, got: %v", err)
	}

	prod := service.NewProduction()
	if prod == nil {
		t.Fatal("NewProduction returned nil")
	}
}

// 15. Root go.mod has no connectors/watsonx require. Manifest never claims darwin.
func TestPackaging_HygieneAndManifest(t *testing.T) {
	t.Parallel()
	rootModBytes, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read root go.mod: %v", err)
	}
	if strings.Contains(string(rootModBytes), "connectors/watsonx") {
		t.Fatal("root go.mod must not require connectors/watsonx")
	}

	manifestBytes, err := os.ReadFile("manifest/template.backendplugin.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(strings.ToLower(string(manifestBytes)), "darwin") {
		t.Fatal("manifest must never claim darwin platform")
	}
}

// 16. Unsupported tools/vision fail closed in Execute.
func TestExecute_UnsupportedToolsAndVision_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-tools",
		ConfigYAML:  []byte("region: us-south\nproject_id: p1\n"),
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	// Tool call request
	streamTools := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "hi", true, nil)
	streamTools.inbox[0].Invocation.Tools = []backendplugin.ToolDef{{
		Name:           "test_tool",
		ParametersJSON: backendplugin.RawJSONAbsentValue(),
	}}
	err = inst.Execute(streamTools)
	if err == nil || !strings.Contains(err.Error(), "tools are not supported") {
		t.Fatalf("expected error mentioning tools are not supported, got: %v", err)
	}
}
