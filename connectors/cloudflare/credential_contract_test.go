package cloudflare_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/cloudflare/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func credSecretReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
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

type credScriptedResponse struct {
	status int
	header map[string]string
	body   string
}

// credScriptedKeys serves every path with per-bearer scripted statuses and
// records every presented bearer in order.
func credScriptedKeys(t *testing.T, script map[string]credScriptedResponse) (*httptest.Server, *[]string) {
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
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(srv.Close)
	return srv, &bearers
}

const credModelsBody = `{"data":[{"id":"model-a"}]}`

func credBaseYAML(srv *httptest.Server) string {
	return "account_id: acc-1\napi_origin: " + srv.URL + "\n"
}

func TestCredentialContract_RejectsYAMLToken(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), credSecretReq(
		"account_id: acc-1\napi_token: hunter2\n",
		map[string]string{"api_token": "host-token"},
	))
	if err == nil {
		t.Fatal("expected literal YAML api_token to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("rejection must name field path and env root: %v", err)
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), credSecretReq("account_id: acc-1\n", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail")
	}
	if !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_LegacyAliasSlotsRejected(t *testing.T) {
	t.Parallel()
	srv, _ := credScriptedKeys(t, map[string]credScriptedResponse{
		"*": {status: 200, body: credModelsBody},
	})
	_, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_key": "legacy-key"},
	))
	if err == nil {
		t.Fatal("expected legacy api_key secret name to be rejected for the api_token slot")
	}
	if !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_FirstTokenPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv, bearers := credScriptedKeys(t, map[string]credScriptedResponse{
		"*": {status: 200, body: credModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "first-token", "api_token_3": "third-token"},
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
	if len(*bearers) != 1 || (*bearers)[0] != "first-token" {
		t.Fatalf("bearers=%q", *bearers)
	}
}

func TestCredentialContract_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := credScriptedKeys(t, map[string]credScriptedResponse{
		"bad-one": {status: 401, body: `{"error":{"message":"invalid"}}`},
		"*":       {status: 200, body: credModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "bad-one", "api_token_2": "good-two"},
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

func TestCredentialContract_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := credScriptedKeys(t, map[string]credScriptedResponse{
		"cooling-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"error":{"message":"quota"}}`},
		"*":           {status: 200, body: credModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "cooling-one", "api_token_2": "next-two"},
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

func TestCredentialContract_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, bearers := credScriptedKeys(t, map[string]credScriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"error":{"message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "primary-token", "api_token_2": "secondary-token"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*bearers) != 1 || (*bearers)[0] != "primary-token" {
		t.Fatalf("short throttle must not consume secondary: bearers=%q", *bearers)
	}
	if strings.Contains(err.Error(), "primary-token") || strings.Contains(err.Error(), "secondary-token") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestCredentialContract_UnknownThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, bearers := credScriptedKeys(t, map[string]credScriptedResponse{
		"*": {status: 429, body: `{"error":{"message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "primary-token", "api_token_2": "secondary-token"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*bearers) != 1 || (*bearers)[0] != "primary-token" {
		t.Fatalf("unknown throttle must not consume secondary: bearers=%q", *bearers)
	}
}

func TestCredentialContract_ExecuteRotatesPastInvalidToken(t *testing.T) {
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
	inst, err := service.New().Configure(context.Background(), credSecretReq(
		credBaseYAML(srv),
		map[string]string{"api_token": "bad-one", "api_token_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	stream := newCredExecuteStream("cloudflare/model-x")
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bearers) != 2 || bearers[0] != "bad-one" || bearers[1] != "good-two" {
		t.Fatalf("bearers=%q", bearers)
	}
}

// credFakeExecuteStream is a minimal in-process ExecuteStream: one START
// frame, then EOF; all server frames are collected for terminal inspection.
type credFakeExecuteStream struct {
	ctx     context.Context
	inv     backendplugin.Invocation
	sent    []backendplugin.ServerFrame
	started bool
}

func newCredExecuteStream(model string) *credFakeExecuteStream {
	text := "hi"
	return &credFakeExecuteStream{
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

func (f *credFakeExecuteStream) Context() context.Context { return f.ctx }

func (f *credFakeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if !f.started {
		f.started = true
		return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: "cred-contract", Invocation: &f.inv}, nil
	}
	return backendplugin.ClientFrame{}, io.EOF
}

func (f *credFakeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	f.sent = append(f.sent, frame)
	return nil
}
