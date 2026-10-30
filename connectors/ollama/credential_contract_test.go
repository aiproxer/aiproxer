package ollama_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/ollama/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func cloudReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKindCloud,
		InstanceID:    "cred-contract",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func localReq(yaml string) backendplugin.ConfigureRequest {
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "local",
		ConfigYAML:    []byte(yaml),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

type scriptedResponse struct {
	status int
	header map[string]string
	body   string
}

// scriptedCloud serves the Ollama Cloud tags endpoint with per-bearer
// scripted statuses and records every presented bearer in order.
func scriptedCloud(t *testing.T, script map[string]scriptedResponse) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var bearers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		bearers = append(bearers, bearer)
		mu.Unlock()
		resp, ok := script[bearer]
		if !ok {
			resp = script["*"]
		}
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		body := resp.body
		if body == "" {
			body = `{"models":[{"name":"llama3-cloud"}]}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &bearers
}

func TestCredentialContract_RejectsYAMLAPIKey(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{service.FactoryKind, service.FactoryKindCloud} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			req := cloudReq("api_key: hunter2\n", map[string]string{"api_key": "host-key"})
			if kind == service.FactoryKind {
				req = localReq("api_key: hunter2\n")
			} else {
				req.FactoryKind = kind
			}
			_, err := service.New().Configure(context.Background(), req)
			if err == nil {
				t.Fatal("expected literal YAML api_key to be rejected")
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("rejection leaks secret value: %v", err)
			}
			if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "OLLAMA_CLOUD_API_KEY") {
				t.Fatalf("rejection must name field path and env root: %v", err)
			}
		})
	}
}

func TestCredentialContract_CloudRequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), cloudReq("base_url: http://127.0.0.1:9\n", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail closed")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "OLLAMA_CLOUD_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_LocalNeedsNoSecrets(t *testing.T) {
	t.Parallel()
	inst, err := service.New().Configure(context.Background(), localReq(""))
	if err != nil {
		t.Fatalf("local no-auth export must configure without secrets: %v", err)
	}
	if inst == nil {
		t.Fatal("expected instance")
	}
}

func TestCredentialContract_CloudFirstKeyPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedCloud(t, map[string]scriptedResponse{
		"*": {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), cloudReq(
		"base_url: "+srv.URL+"\nmodels_url: "+srv.URL+"/api/tags\n",
		map[string]string{"api_key": "first-key", "api_key_3": "third-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].NativeModelID != "llama3" {
		t.Fatalf("listed=%+v", listed.Models)
	}
	if len(*bearers) != 1 || (*bearers)[0] != "first-key" {
		t.Fatalf("bearers=%q", *bearers)
	}
}

func TestCredentialContract_CloudAuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedCloud(t, map[string]scriptedResponse{
		"bad-one": {status: 401, body: `{"error":"invalid"}`},
		"*":       {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), cloudReq(
		"base_url: "+srv.URL+"\nmodels_url: "+srv.URL+"/api/tags\n",
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(*bearers) != 2 || (*bearers)[0] != "bad-one" || (*bearers)[1] != "good-two" {
		t.Fatalf("bearers=%q", *bearers)
	}
}

func TestCredentialContract_CloudShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedCloud(t, map[string]scriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"error":"slow"}`},
	})
	inst, err := service.New().Configure(context.Background(), cloudReq(
		"base_url: "+srv.URL+"\nmodels_url: "+srv.URL+"/api/tags\n",
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "primary-key") || strings.Contains(err.Error(), "secondary-key") {
		t.Fatalf("error leaks secret: %v", err)
	}
	for _, bearer := range *bearers {
		if bearer == "secondary-key" {
			t.Fatalf("short throttle must not consume secondary: bearers=%q", *bearers)
		}
	}
}

func TestCredentialContract_CloudLongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedCloud(t, map[string]scriptedResponse{
		"cooling-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"error":"quota"}`},
		"*":           {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), cloudReq(
		"base_url: "+srv.URL+"\nmodels_url: "+srv.URL+"/api/tags\n",
		map[string]string{"api_key": "cooling-one", "api_key_2": "next-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(*bearers) != 2 {
		t.Fatalf("bearers=%q", *bearers)
	}
}

func TestCredentialContract_CloudExecuteRotatesPastInvalidKey(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var bearers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		bearers = append(bearers, bearer)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if bearer == "bad-one" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), cloudReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	stream := newExecuteStream("ollama-cloud/llama3")
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bearers) != 2 || bearers[0] != "bad-one" || bearers[1] != "good-two" {
		t.Fatalf("bearers=%q", bearers)
	}
}

// fakeExecuteStream is a minimal in-process ExecuteStream: one START frame,
// then EOF; all server frames are collected for terminal inspection.
type fakeExecuteStream struct {
	ctx     context.Context
	inv     backendplugin.Invocation
	sent    []backendplugin.ServerFrame
	started bool
}

func newExecuteStream(model string) *fakeExecuteStream {
	text := "hi"
	return &fakeExecuteStream{
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

func (f *fakeExecuteStream) Context() context.Context { return f.ctx }

func (f *fakeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if !f.started {
		f.started = true
		return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: "cred-contract", Invocation: &f.inv}, nil
	}
	return backendplugin.ClientFrame{}, io.EOF
}

func (f *fakeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	f.sent = append(f.sent, frame)
	return nil
}
