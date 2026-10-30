package watsonx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/watsonx/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func wxSecretReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
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

// wxScriptedServer emulates the IBM IAM token endpoint and the watsonx ML
// chat endpoint. IAM exchanges one bearer per api_key; ML serves per-bearer
// scripted statuses. Every presented api_key and bearer is recorded in order.
type wxScriptedServer struct {
	t        *testing.T
	mu       sync.Mutex
	apiKeys  []string
	bearers  []string
	iam      map[string]scriptedIAM
	ml       map[string]wxScriptedResponse
	srv      *httptest.Server
	chatPath string
}

type scriptedIAM struct {
	status int
	body   string
}

type wxScriptedResponse struct {
	status int
	header map[string]string
	body   string
}

func newWXScriptedServer(t *testing.T, iam map[string]scriptedIAM, ml map[string]wxScriptedResponse) *wxScriptedServer {
	t.Helper()
	s := &wxScriptedServer{t: t, iam: iam, ml: ml, chatPath: "/ml/v1/text/chat"}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *wxScriptedServer) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/identity/token":
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		apiKey := vals.Get("apikey")
		s.mu.Lock()
		s.apiKeys = append(s.apiKeys, apiKey)
		s.mu.Unlock()
		resp, ok := s.iam[apiKey]
		if !ok {
			resp = s.iam["*"]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
		return
	case s.chatPath:
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		s.bearers = append(s.bearers, bearer)
		s.mu.Unlock()
		resp, ok := s.ml[bearer]
		if !ok {
			resp = s.ml["*"]
		}
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
		return
	default:
		http.NotFound(w, r)
	}
}

func iamOK(bearer string) scriptedIAM {
	return scriptedIAM{status: 200, body: fmt.Sprintf(`{"access_token": %q, "token_type": "Bearer", "expires_in": 3600}`, bearer)}
}

func wxBaseYAML(s *wxScriptedServer) string {
	return fmt.Sprintf("region: us-south\nproject_id: contract-proj\napi_origin: %s\niam_origin: %s\nmodel_id: ibm/granite-3-8b-instruct\n", s.srv.URL, s.srv.URL)
}

const wxChatBody = `{"id":"chat-1","choices":[{"message":{"content":"hi"}}]}`

func wxExecute(inst backendplugin.ConfiguredInstance) error {
	stream := newTestExecuteStream(context.Background(), "watsonx/ibm/granite-3-8b-instruct", aipapi.OperationOpenAIChatCompletions, "hi", true, nil)
	return inst.Execute(stream)
}

func TestCredentialContract_RejectsYAMLKey(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), wxSecretReq(
		"region: us-south\nproject_id: p1\nmodel_id: m1\napi_key: hunter2\n",
		map[string]string{"api_key": "host-key"},
	))
	if err == nil {
		t.Fatal("expected literal YAML api_key to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "WATSONX_API_KEY") {
		t.Fatalf("rejection must name field path and env root: %v", err)
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), wxSecretReq(
		"region: us-south\nproject_id: p1\nmodel_id: m1\n", nil,
	))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "WATSONX_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_FirstKeyPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv := newWXScriptedServer(t,
		map[string]scriptedIAM{"*": iamOK("iam-first")},
		map[string]wxScriptedResponse{"*": {status: 200, body: wxChatBody}},
	)
	inst, err := service.New().Configure(context.Background(), wxSecretReq(
		wxBaseYAML(srv),
		map[string]string{"api_key": "first-key", "api_key_3": "third-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := wxExecute(inst); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.apiKeys) != 1 || srv.apiKeys[0] != "first-key" {
		t.Fatalf("apiKeys=%q", srv.apiKeys)
	}
	if len(srv.bearers) != 1 || srv.bearers[0] != "iam-first" {
		t.Fatalf("bearers=%q", srv.bearers)
	}
}

func TestCredentialContract_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv := newWXScriptedServer(t,
		map[string]scriptedIAM{
			"bad-one": {status: 401, body: `{"error": "invalid apikey"}`},
			"*":       iamOK("iam-good"),
		},
		map[string]wxScriptedResponse{"*": {status: 200, body: wxChatBody}},
	)
	inst, err := service.New().Configure(context.Background(), wxSecretReq(
		wxBaseYAML(srv),
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := wxExecute(inst); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.apiKeys) != 2 || srv.apiKeys[0] != "bad-one" || srv.apiKeys[1] != "good-two" {
		t.Fatalf("apiKeys=%q", srv.apiKeys)
	}
	if len(srv.bearers) != 1 || srv.bearers[0] != "iam-good" {
		t.Fatalf("bearers=%q", srv.bearers)
	}
}

func TestCredentialContract_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv := newWXScriptedServer(t,
		map[string]scriptedIAM{
			"cooling-one": iamOK("iam-one"),
			"*":           iamOK("iam-two"),
		},
		map[string]wxScriptedResponse{
			"iam-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"error":"quota"}`},
			"*":       {status: 200, body: wxChatBody},
		},
	)
	// The ML 429 for the first slot's bearer advances selection: the second
	// slot exchanges its own authenticating key and succeeds.
	inst, err := service.New().Configure(context.Background(), wxSecretReq(
		wxBaseYAML(srv),
		map[string]string{"api_key": "cooling-one", "api_key_2": "next-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := wxExecute(inst); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.apiKeys) != 2 {
		t.Fatalf("long throttle must exchange the second slot: apiKeys=%q", srv.apiKeys)
	}
	if len(srv.bearers) != 2 || srv.bearers[0] != "iam-one" || srv.bearers[1] != "iam-two" {
		t.Fatalf("bearers=%q", srv.bearers)
	}
}

func TestCredentialContract_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv := newWXScriptedServer(t,
		map[string]scriptedIAM{"*": iamOK("iam-primary")},
		map[string]wxScriptedResponse{
			"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"error":"slow"}`},
		},
	)
	inst, err := service.New().Configure(context.Background(), wxSecretReq(
		wxBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	err = wxExecute(inst)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.apiKeys) != 1 || srv.apiKeys[0] != "primary-key" {
		t.Fatalf("short throttle must not exchange secondary: apiKeys=%q", srv.apiKeys)
	}
	if len(srv.bearers) != 1 || srv.bearers[0] != "iam-primary" {
		t.Fatalf("bearers=%q", srv.bearers)
	}
	if strings.Contains(err.Error(), "primary-key") || strings.Contains(err.Error(), "secondary-key") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestCredentialContract_UnknownThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv := newWXScriptedServer(t,
		map[string]scriptedIAM{"*": iamOK("iam-primary")},
		map[string]wxScriptedResponse{
			"*": {status: 429, body: `{"error":"slow"}`},
		},
	)
	inst, err := service.New().Configure(context.Background(), wxSecretReq(
		wxBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	err = wxExecute(inst)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.apiKeys) != 1 {
		t.Fatalf("unknown throttle must not exchange secondary: apiKeys=%q", srv.apiKeys)
	}
	if len(srv.bearers) != 1 {
		t.Fatalf("bearers=%q", srv.bearers)
	}
}
