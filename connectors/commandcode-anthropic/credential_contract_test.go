package commandcodeanthropic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func secretReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
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

type scriptedResponse struct {
	status int
	header map[string]string
	body   string
}

// scriptedKeys serves the Anthropic wire (/models, /messages) with per-key
// scripted statuses and records every presented key in order. The client
// sends both Authorization bearer and x-api-key; the bearer is recorded.
func scriptedKeys(t *testing.T, script map[string]scriptedResponse) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("x-api-key")
		}
		mu.Lock()
		keys = append(keys, key)
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
		body := resp.body
		if body == "" {
			if strings.HasSuffix(r.URL.Path, "/models") {
				body = `{"data":[{"id":"claude-x"}]}`
			} else {
				body = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
			}
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &keys
}

func TestCredentialContract_RejectsYAMLAPIKey(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), secretReq(
		"api_key: hunter2\n",
		map[string]string{"api_key": "host-key"},
	))
	if err == nil {
		t.Fatal("expected literal YAML api_key to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "COMMANDCODE_ANTHROPIC_API_KEY") {
		t.Fatalf("rejection must name field path and env root: %v", err)
	}
}

func TestCredentialContract_RejectsInterpolationAndSharedRoot(t *testing.T) {
	t.Setenv("SOME_INJECTED_KEY", "injected-secret")
	t.Setenv("COMMANDCODE_API_KEY", "shared-root-secret")
	// Interpolation syntax is itself a forbidden literal: it must be rejected,
	// not expanded against connector-visible environment.
	_, err := service.New().Configure(context.Background(), secretReq(
		"api_key: ${SOME_INJECTED_KEY}\n",
		map[string]string{"api_key": "host-key"},
	))
	if err == nil {
		t.Fatal("expected interpolated YAML api_key to be rejected")
	}
	// The legacy shared root must not authenticate this export.
	req := secretReq("base_url: http://127.0.0.1:9\n", nil)
	_, err = service.New().Configure(context.Background(), req)
	if err == nil {
		t.Fatal("shared COMMANDCODE_API_KEY must not authenticate the anthropic export")
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), secretReq("base_url: http://127.0.0.1:9\n", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "COMMANDCODE_ANTHROPIC_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_FirstKeyPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv, keys := scriptedKeys(t, map[string]scriptedResponse{
		"*": {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "first-key", "api_key_3": "third-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 {
		t.Fatalf("listed=%+v", listed.Models)
	}
	if len(*keys) != 1 || (*keys)[0] != "first-key" {
		t.Fatalf("keys=%q", *keys)
	}
}

func TestCredentialContract_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, keys := scriptedKeys(t, map[string]scriptedResponse{
		"bad-one": {status: 401, body: `{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`},
		"*":       {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(*keys) != 2 || (*keys)[0] != "bad-one" || (*keys)[1] != "good-two" {
		t.Fatalf("keys=%q", *keys)
	}
}

func TestCredentialContract_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv, keys := scriptedKeys(t, map[string]scriptedResponse{
		"cooling-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"type":"error","error":{"type":"rate_limit_error","message":"quota"}}`},
		"*":           {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "cooling-one", "api_key_2": "next-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(*keys) != 2 {
		t.Fatalf("keys=%q", *keys)
	}
}

func TestCredentialContract_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, keys := scriptedKeys(t, map[string]scriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*keys) != 1 || (*keys)[0] != "primary-key" {
		t.Fatalf("short throttle must not consume secondary: keys=%q", *keys)
	}
	if strings.Contains(err.Error(), "primary-key") || strings.Contains(err.Error(), "secondary-key") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestCredentialContract_ExecuteRotatesPastInvalidKey(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		keys = append(keys, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if key == "bad-one" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), secretReq(
		"base_url: "+srv.URL+"\n",
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	stream := newExecuteStream("commandcode-anthropic/claude-3-5-sonnet-20241022")
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] != "bad-one" || keys[1] != "good-two" {
		t.Fatalf("keys=%q", keys)
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
