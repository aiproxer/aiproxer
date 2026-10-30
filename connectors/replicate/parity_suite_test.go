package replicate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/replicate/internal/service"
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

// 1. Missing model / inference_contract fail closed; unknown contract fail closed.
func TestConfig_Validation_FailClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		err  string
	}{
		{
			name: "missing model",
			yaml: "inference_contract: prompt-text\n",
			err:  "model is required",
		},
		{
			name: "model without slash",
			yaml: "model: llama-3\ninference_contract: prompt-text\n",
			err:  "owner/name format",
		},
		{
			name: "model with full prediction url",
			yaml: "model: https://api.replicate.com/v1/models/meta/llama-3/predictions\ninference_contract: prompt-text\n",
			err:  "full prediction URLs are rejected",
		},
		{
			name: "missing inference_contract",
			yaml: "model: meta/llama-3\n",
			err:  "inference_contract is required",
		},
		{
			name: "unknown inference_contract",
			yaml: "model: meta/llama-3\ninference_contract: chat-completions\n",
			err:  "unsupported inference_contract",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := service.ParseConfigYAML([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.err)
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("expected error containing %q, got: %v", tc.err, err)
			}
		})
	}
}

// 2. YAML token rejected.
func TestConfigure_YAMLSecrets_Rejected(t *testing.T) {
	t.Parallel()
	svc := service.New()
	sec := backendplugin.SecretBundle{
		Values: map[string][]byte{"api_token": []byte("valid-tok")},
	}

	badYAMLs := []struct {
		name string
		yaml string
	}{
		{"api_token in yaml", "model: meta/llama-3\ninference_contract: prompt-text\napi_token: secret-token\n"},
		{"token in yaml", "model: meta/llama-3\ninference_contract: prompt-text\ntoken: secret-token\n"},
		{"api_key in yaml", "model: meta/llama-3\ninference_contract: prompt-text\napi_key: secret-key\n"},
		{"apikey in yaml", "model: meta/llama-3\ninference_contract: prompt-text\napikey: secret-key\n"},
		{"secret in yaml", "model: meta/llama-3\ninference_contract: prompt-text\nsecret: secret-val\n"},
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
			if !strings.Contains(err.Error(), "is forbidden") || !strings.Contains(err.Error(), "REPLICATE_API_TOKEN") {
				t.Fatalf("expected forbidden-field error naming env root, got: %v", err)
			}
		})
	}
}

// 3. Configure->Execute create hits /v1/models/{owner}/{name}/predictions with input.prompt and Bearer.
func TestExecute_Create_EndpointAndHeaders(t *testing.T) {
	t.Parallel()

	var calledPath string
	var authHeader string
	var contentType string
	var reqBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		authHeader = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")

		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &reqBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p1","status":"succeeded","output":"test response"}`))
	}))
	defer srv.Close()

	svc := service.NewProduction()
	cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: meta/llama-3-70b-instruct\ninference_contract: prompt-text\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("my-secret-replicate-token")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "replicate/meta/llama-3-70b-instruct", aipapi.OperationOpenAIChatCompletions, "Hello Replicate", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if calledPath != "/v1/models/meta/llama-3-70b-instruct/predictions" {
		t.Fatalf("unexpected path: %s", calledPath)
	}
	if authHeader != "Bearer my-secret-replicate-token" {
		t.Fatalf("unexpected auth: %s", authHeader)
	}
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("unexpected content type: %s", contentType)
	}

	input, ok := reqBody["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected input map, got %v", reqBody)
	}
	if input["prompt"] != "Hello Replicate" {
		t.Fatalf("expected prompt 'Hello Replicate', got %v", input["prompt"])
	}
}

// 4. Immediate succeeded + string output -> text delta.
func TestExecute_ImmediateSucceeded_OutputDelta(t *testing.T) {
	t.Parallel()

	t.Run("string output", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"p2","status":"succeeded","output":"Hello immediate world"}`))
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		stream := newTestExecuteStream(context.Background(), "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "hi", true)
		if err := inst.Execute(stream); err != nil {
			t.Fatalf("execute: %v", err)
		}

		var deltas []string
		for _, f := range stream.outbox {
			if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
				deltas = append(deltas, *f.Event.Delta)
			}
		}
		if len(deltas) != 1 || deltas[0] != "Hello immediate world" {
			t.Fatalf("expected ['Hello immediate world'], got %v", deltas)
		}
	})

	t.Run("array of strings output joined", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"p3","status":"succeeded","output":["chunk1", " ", "chunk2"]}`))
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		stream := newTestExecuteStream(context.Background(), "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "hi", true)
		if err := inst.Execute(stream); err != nil {
			t.Fatalf("execute: %v", err)
		}

		var deltas []string
		for _, f := range stream.outbox {
			if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
				deltas = append(deltas, *f.Event.Delta)
			}
		}
		if len(deltas) != 1 || deltas[0] != "chunk1 chunk2" {
			t.Fatalf("expected ['chunk1 chunk2'], got %v", deltas)
		}
	})

	t.Run("non-string output fails closed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"p4","status":"succeeded","output":{"image_url":"https://replicate.delivery/pbxt/foo.png"}}`))
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		stream := newTestExecuteStream(context.Background(), "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "hi", true)
		err = inst.Execute(stream)
		if err == nil {
			t.Fatal("expected failure for non-string output, got nil")
		}
		if !strings.Contains(err.Error(), "unexpected output type") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})
}

// 5. Poll path: create returns starting, GET prediction then succeeded.
func TestExecute_PollPath_StartingToSucceeded(t *testing.T) {
	t.Parallel()

	var pollHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models/test/model/predictions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"id": "poll-pred",
				"status": "starting",
				"urls": {
					"get": "/v1/predictions/poll-pred",
					"cancel": "/v1/predictions/poll-pred/cancel"
				}
			}`))
		case "/v1/predictions/poll-pred":
			hits := pollHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if hits < 2 {
				_, _ = w.Write([]byte(`{"id":"poll-pred","status":"processing"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"poll-pred","status":"succeeded","output":"poll succeeded text"}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := service.New()
	cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\npoll_interval: 10ms\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),

		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("tok")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "poll test", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if pollHits.Load() < 2 {
		t.Fatalf("expected at least 2 poll hits, got %d", pollHits.Load())
	}

	var deltas []string
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
			deltas = append(deltas, *f.Event.Delta)
		}
	}
	if len(deltas) != 1 || deltas[0] != "poll succeeded text" {
		t.Fatalf("expected ['poll succeeded text'], got %v", deltas)
	}
}

// 6. Stream path: create returns urls.stream; GET stream SSE; text deltas.
func TestExecute_StreamPath_SSE(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models/test/model/predictions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(fmt.Appendf(nil, `{
				"id": "stream-pred",
				"status": "processing",
				"urls": {
					"get": "/v1/predictions/stream-pred",
					"cancel": "/v1/predictions/stream-pred/cancel",
					"stream": "/v1/predictions/stream-pred/stream"
				}
			}`))
		case "/v1/predictions/stream-pred/stream":
			if r.Header.Get("Accept") != "text/event-stream" {
				http.Error(w, "expected text/event-stream", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			chunks := []string{
				"event: output\ndata: Hello\n\n",
				"event: output\ndata:  stream\n\n",
				"event: output\ndata:  world\n\n",
				"event: done\ndata: {}\n\n",
			}
			for _, c := range chunks {
				_, _ = w.Write([]byte(c))
				if ok {
					flusher.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := service.New()
	cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),

		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("tok")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "stream test", false)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var deltas []string
	for _, f := range stream.outbox {
		if f.Kind == backendplugin.ServerFrameEvent && f.Event != nil && f.Event.Kind == backendplugin.EventTextDelta && f.Event.Delta != nil {
			deltas = append(deltas, *f.Event.Delta)
		}
	}
	expected := []string{"Hello", " stream", " world"}
	if len(deltas) != len(expected) {
		t.Fatalf("expected deltas %v, got %v", expected, deltas)
	}
	for i := range expected {
		if deltas[i] != expected[i] {
			t.Fatalf("delta[%d]: want %q got %q", i, expected[i], deltas[i])
		}
	}
}

// 7. Context cancel during poll/stream -> cancel POST called.
func TestExecute_ContextCancel_CallsCancelEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("cancel during poll", func(t *testing.T) {
		t.Parallel()
		var cancelHits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/models/test/model/predictions":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{
					"id": "poll-cancel-pred",
					"status": "processing",
					"urls": {
						"get": "/v1/predictions/poll-cancel-pred",
						"cancel": "/v1/predictions/poll-cancel-pred/cancel"
					}
				}`))
			case "/v1/predictions/poll-cancel-pred":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"poll-cancel-pred","status":"processing"}`))
			case "/v1/predictions/poll-cancel-pred/cancel":
				if r.Method == http.MethodPost {
					cancelHits.Add(1)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"canceled"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\npoll_interval: 20ms\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		stream := newTestExecuteStream(ctx, "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "cancel test", true)
		_ = inst.Execute(stream)

		deadline := time.Now().Add(2 * time.Second)
		for cancelHits.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if cancelHits.Load() == 0 {
			t.Fatal("expected cancel endpoint POST during poll cancellation, got 0 hits")
		}
	})

	t.Run("cancel during stream", func(t *testing.T) {
		t.Parallel()
		var cancelHits atomic.Int32
		streamConnected := make(chan struct{})

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/models/test/model/predictions":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{
					"id": "stream-cancel-pred",
					"status": "processing",
					"urls": {
						"cancel": "/v1/predictions/stream-cancel-pred/cancel",
						"stream": "/v1/predictions/stream-cancel-pred/stream"
					}
				}`))
			case "/v1/predictions/stream-cancel-pred/stream":
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if ok {
					flusher.Flush()
				}
				_, _ = w.Write([]byte("event: output\ndata: first chunk\n\n"))
				if ok {
					flusher.Flush()
				}
				close(streamConnected)
				// Hold connection open until client disconnects
				<-r.Context().Done()
			case "/v1/predictions/stream-cancel-pred/cancel":
				if r.Method == http.MethodPost {
					cancelHits.Add(1)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"canceled"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: test/model\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-streamConnected
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()

		stream := newTestExecuteStream(ctx, "replicate/test/model", aipapi.OperationOpenAIChatCompletions, "cancel test", false)
		_ = inst.Execute(stream)

		deadline := time.Now().Add(2 * time.Second)
		for cancelHits.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if cancelHits.Load() == 0 {
			t.Fatal("expected cancel endpoint POST during stream cancellation, got 0 hits")
		}
	})
}

// 8. ListModels only configured model; GET model 404 fails closed.
func TestListModels_OnlyConfiguredModel_FailsClosedOn404(t *testing.T) {
	t.Parallel()

	t.Run("success returns single configured model", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/models/meta/llama-3-70b-instruct" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"name":"llama-3-70b-instruct","owner":"meta"}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: meta/llama-3-70b-instruct\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		lm, err := inst.ListModels(context.Background(), 10)
		if err != nil {
			t.Fatalf("list models: %v", err)
		}
		if len(lm.Models) != 1 {
			t.Fatalf("expected 1 model, got %d", len(lm.Models))
		}
		m := lm.Models[0]
		if m.CanonicalModelID != "replicate/meta/llama-3-70b-instruct" {
			t.Fatalf("canonical model id want replicate/meta/llama-3-70b-instruct got %q", m.CanonicalModelID)
		}
		if m.NativeModelID != "meta/llama-3-70b-instruct" {
			t.Fatalf("native model id want meta/llama-3-70b-instruct got %q", m.NativeModelID)
		}
		if m.FactoryKind != service.FactoryKind {
			t.Fatalf("factory kind want replicate got %q", m.FactoryKind)
		}
		if lm.FetchedUnixMS == 0 {
			t.Fatal("expected real non-zero FetchedUnixMS")
		}
	})

	t.Run("404 fails closed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer srv.Close()

		svc := service.New()
		cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: missing/model\ninference_contract: prompt-text\n", srv.URL)
		inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),

			Secrets: backendplugin.SecretBundle{
				Values: map[string][]byte{"api_token": []byte("tok")},
			},
		})
		if err != nil {
			t.Fatalf("configure: %v", err)
		}

		_, err = inst.ListModels(context.Background(), 10)
		if err == nil {
			t.Fatal("expected 404 to fail closed, got nil error")
		}
		if !strings.Contains(err.Error(), "verification failed") && !strings.Contains(err.Error(), "404") {
			t.Fatalf("expected error mentioning verification failure/404, got: %v", err)
		}
	})
}

// 9. Execute of a listed-but-not-configured other id fails closed.
func TestExecute_UnconfiguredModel_FailsClosed(t *testing.T) {
	t.Parallel()

	svc := service.New()
	cfgYAML := "api_origin: https://mock.example.com\nmodel: meta/llama-3\ninference_contract: prompt-text\n"
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),

		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("tok")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "replicate/other/model", aipapi.OperationOpenAIChatCompletions, "hi", true)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected execute of unconfigured model to fail, got nil")
	}
	if !strings.Contains(err.Error(), "does not match configured model") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// 10. Hard-negative vs chat/completions.
func TestExecute_HardNegative_OpenAIPathUnused(t *testing.T) {
	t.Parallel()

	var chatCompletionsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			chatCompletionsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"openai fallback"}}]}`))
			return
		}
		// Return 404 for prediction paths
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc := service.New()
	cfgYAML := fmt.Sprintf("api_origin: %s\nmodel: meta/llama-3\ninference_contract: prompt-text\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),

		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("tok")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "replicate/meta/llama-3", aipapi.OperationOpenAIChatCompletions, "hi", true)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected execute to fail when predictions 404s, got nil")
	}

	if chatCompletionsHits.Load() != 0 {
		t.Fatalf("hard-negative violation: chat/completions was called %d times", chatCompletionsHits.Load())
	}
}

// 11. Tools fail closed.
func TestExecute_Tools_FailClosed(t *testing.T) {
	t.Parallel()

	svc := service.New()
	cfgYAML := "api_origin: https://mock.example.com\nmodel: meta/llama-3\ninference_contract: prompt-text\n"
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte(cfgYAML),

		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"api_token": []byte("tok")},
		},
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}

	msg := "call tool"
	inv := backendplugin.Invocation{
		RequestID:        "req-tools",
		AttemptID:        "att-tools",
		ALegID:           "al-tools",
		BLegID:           "bl-tools",
		CanonicalModelID: "replicate/meta/llama-3",
		NativeModelID:    "meta/llama-3",
		Operation:        string(aipapi.OperationOpenAIChatCompletions),
		DeliveryMode:     string(aipapi.DeliveryModeStreaming),
		TransportMode:    string(aipapi.TransportModeStreaming),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
		Tools: []backendplugin.ToolDef{{
			Name:           "weather_lookup",
			ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`)),
		}},
	}
	stream := &memStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}

	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected tools to fail closed, got nil")
	}
	if !strings.Contains(err.Error(), "tools are not supported") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// 12. Describe kind replicate. Tools/Vision false. Streaming true if stream path implemented.
func TestService_Describe(t *testing.T) {
	t.Parallel()

	svc := service.NewProduction()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}

	if desc.PluginID != service.PluginID {
		t.Fatalf("want plugin id %q got %q", service.PluginID, desc.PluginID)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	f := desc.Factories[0]
	if f.Kind != service.FactoryKind {
		t.Fatalf("want kind %q got %q", service.FactoryKind, f.Kind)
	}
	if !f.StaticCapabilities.Streaming {
		t.Fatal("expected StaticCapabilities.Streaming to be true")
	}
	if f.StaticCapabilities.Tools {
		t.Fatal("expected StaticCapabilities.Tools to be false")
	}
	if f.StaticCapabilities.Vision {
		t.Fatal("expected StaticCapabilities.Vision to be false")
	}
}

// 14. New() without token fails with NewProduction guidance; no darwin; root go.mod clean.
func TestHygiene_NewGuidance_NoDarwin_RootClean(t *testing.T) {
	t.Parallel()

	t.Run("New() without host secrets fails closed naming slot and env root", func(t *testing.T) {
		t.Parallel()
		svc := service.New()
		cfgYAML := "model: meta/llama-3\ninference_contract: prompt-text\n"
		_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),
		})
		if err == nil {
			t.Fatal("expected error for New() without host secrets, got nil")
		}
		if !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "REPLICATE_API_TOKEN") {
			t.Fatalf("expected error naming slot and env root, got: %v", err)
		}
	})

	t.Run("manifest template rejects darwin", func(t *testing.T) {
		t.Parallel()
		manifestBytes, err := os.ReadFile(filepath.Join("manifest", "template.backendplugin.json"))
		if err != nil {
			t.Fatalf("read manifest template: %v", err)
		}
		var m struct {
			Platforms []struct {
				OS   string `json:"os"`
				Arch string `json:"arch"`
			} `json:"platforms"`
		}
		if err := json.Unmarshal(manifestBytes, &m); err != nil {
			t.Fatalf("unmarshal manifest: %v", err)
		}
		for _, p := range m.Platforms {
			if strings.EqualFold(p.OS, "darwin") {
				t.Fatalf("manifest template must not declare darwin platform: %+v", p)
			}
		}
	})

	t.Run("root go.mod does not contain connectors/replicate", func(t *testing.T) {
		t.Parallel()
		rootModBytes, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
		if err != nil {
			t.Fatalf("read root go.mod: %v", err)
		}
		if strings.Contains(string(rootModBytes), "connectors/replicate") {
			t.Fatal("root go.mod must not require or reference connectors/replicate")
		}
	})
}
