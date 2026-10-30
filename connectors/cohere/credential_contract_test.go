package cohere_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/cohere/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func coSecretReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
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

type coScriptedResponse struct {
	status int
	header map[string]string
	body   string
}

// coScriptedKeys serves every path with per-bearer scripted statuses and
// records every presented bearer in order.
func coScriptedKeys(t *testing.T, script map[string]coScriptedResponse) (*httptest.Server, *[]string) {
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

const coModelsBody = `{"models":[{"name":"command-r","endpoints":["chat"]}]}`

const coChatBody = `{"id":"c-1","finish_reason":"COMPLETE","message":{"role":"assistant","content":"ok"}}`

func coBaseYAML(srv *httptest.Server) string {
	return fmt.Sprintf("api_origin: %s\nmodel: command-r\n", srv.URL)
}

func TestCredentialContract_RejectsYAMLKey(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), coSecretReq(
		"model: command-r\napi_key: hunter2\n",
		map[string]string{"api_key": "host-key"},
	))
	if err == nil {
		t.Fatal("expected literal YAML api_key to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "COHERE_API_KEY") {
		t.Fatalf("rejection must name field path and env root: %v", err)
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), coSecretReq("model: command-r\n", nil))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "COHERE_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_FirstKeyPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"*": {status: 200, body: coModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
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
	if len(*bearers) != 1 || (*bearers)[0] != "first-key" {
		t.Fatalf("bearers=%q", *bearers)
	}
}

func TestCredentialContract_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"bad-one": {status: 401, body: `{"message":"invalid api key"}`},
		"*":       {status: 200, body: coModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
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

func TestCredentialContract_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"cooling-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"message":"quota"}`},
		"*":           {status: 200, body: coModelsBody},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
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

func TestCredentialContract_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"message":"slow"}`},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*bearers) != 1 || (*bearers)[0] != "primary-key" {
		t.Fatalf("short throttle must not consume secondary: bearers=%q", *bearers)
	}
	if strings.Contains(err.Error(), "primary-key") || strings.Contains(err.Error(), "secondary-key") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestCredentialContract_UnknownThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"*": {status: 429, body: `{"message":"slow"}`},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*bearers) != 1 || (*bearers)[0] != "primary-key" {
		t.Fatalf("unknown throttle must not consume secondary: bearers=%q", *bearers)
	}
}

func TestCredentialContract_ExecuteRotatesPastInvalidKey(t *testing.T) {
	t.Parallel()
	srv, bearers := coScriptedKeys(t, map[string]coScriptedResponse{
		"bad-one": {status: 401, body: `{"message":"invalid api key"}`},
		"*":       {status: 200, body: coChatBody},
	})
	inst, err := service.New().Configure(context.Background(), coSecretReq(
		coBaseYAML(srv),
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	stream := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIChatCompletions, "hi", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	if len(*bearers) != 2 || (*bearers)[0] != "bad-one" || (*bearers)[1] != "good-two" {
		t.Fatalf("bearers=%q", *bearers)
	}
}
