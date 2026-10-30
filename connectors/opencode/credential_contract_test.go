package opencode_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func secretReq(kind, yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind:   kind,
		InstanceID:    "cred-contract",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func kindEnvRoot(kind string) string {
	if kind == service.FactoryKindZen {
		return "OPENCODE_ZEN_API_KEY"
	}
	return "OPENCODE_GO_API_KEY"
}

func TestCredentialContract_RejectsYAMLAPIKey(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{service.FactoryKindGo, service.FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, yaml := range []string{"api_key: hunter2\n", "auth_json_path: /tmp/auth.json\n"} {
				_, err := service.New().Configure(context.Background(), secretReq(kind, yaml,
					map[string]string{"api_key": "host-key"}))
				if err == nil {
					t.Fatalf("expected literal YAML secret field to be rejected: %q", yaml)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("rejection leaks secret value: %v", err)
				}
				if !strings.Contains(err.Error(), "api_key") && !strings.Contains(err.Error(), "auth_json") {
					t.Fatalf("rejection must name field path: %v", err)
				}
				if !strings.Contains(err.Error(), kindEnvRoot(kind)) {
					t.Fatalf("rejection must name env root: %v", err)
				}
			}
		})
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	for _, kind := range []string{service.FactoryKindGo, service.FactoryKindZen} {
		t.Run(kind, func(t *testing.T) {
			_, err := service.New().Configure(context.Background(), secretReq(kind,
				"base_url: http://127.0.0.1:9\n", nil))
			if err == nil {
				t.Fatal("expected missing Configure secrets to fail")
			}
			if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), kindEnvRoot(kind)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCredentialContract_AuthJSONDecoyIgnored(t *testing.T) {
	// A decoy auth.json must never authenticate: without host secrets
	// Configure fails, and with host secrets the file content is never used.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENCODE_GO_API_KEY", "env-decoy-key")
	t.Setenv("OPENCODE_ZEN_API_KEY", "env-decoy-key")
	t.Setenv("OPENCODE_API_KEY", "env-decoy-key")
	decoy := `{"opencode-go": {"type": "api", "key": "file-decoy-key"}}`
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"), []byte(decoy), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: http://127.0.0.1:9\n", nil)); err == nil {
		t.Fatal("file/env fallback must not authenticate: host Configure secrets are required")
	}

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: "+srv.URL+"\n", map[string]string{"api_key": "host-secret-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer host-secret-key" {
		t.Fatalf("auth=%q (file/env fallback leaked in)", auth)
	}
}

func TestCredentialContract_GoZenScopesDistinct(t *testing.T) {
	t.Parallel()
	goSrv, goAuth := opencodeModelServer(t, `{"data":[{"id":"kimi-k2.7-code"}]}`)
	zenSrv, zenAuth := opencodeModelServer(t, `{"data":[{"id":"gpt-5.4"}]}`)
	goInst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: "+goSrv.URL+"\n", map[string]string{"api_key": "go-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	zenInst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindZen,
		"base_url: "+zenSrv.URL+"\n", map[string]string{"api_key": "zen-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := goInst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := zenInst.ListModels(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if *goAuth != "Bearer go-secret" {
		t.Fatalf("go auth=%q", *goAuth)
	}
	if *zenAuth != "Bearer zen-secret" {
		t.Fatalf("zen auth=%q", *zenAuth)
	}
}

// scriptedModels serves /models with per-bearer scripted statuses and
// records every presented bearer in order.
func scriptedModels(t *testing.T, script map[string]scriptedResponse) (*httptest.Server, *[]string) {
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
			body = `{"data":[{"id":"m"}]}`
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &bearers
}

type scriptedResponse struct {
	status int
	header map[string]string
	body   string
}

func TestCredentialContract_InventoryAuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedModels(t, map[string]scriptedResponse{
		"bad-one": {status: 401, body: `{"error":{"message":"invalid"}}`},
		"*":       {status: 200},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: "+srv.URL+"\n", map[string]string{"api_key": "bad-one", "api_key_2": "good-two"}))
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

func TestCredentialContract_InventoryShortThrottleStays(t *testing.T) {
	t.Parallel()
	srv, bearers := scriptedModels(t, map[string]scriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"error":{"message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: "+srv.URL+"\n", map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected inventory to report the throttle")
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

func TestCredentialContract_ExecuteRotatesPastInvalidKey(t *testing.T) {
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
	inst, err := service.New().Configure(context.Background(), secretReq(service.FactoryKindGo,
		"base_url: "+srv.URL+"\nmodels:\n  - id: emu-model\n    endpoint: "+srv.URL+"/v1/chat/completions\n    ai_sdk_package: \"@ai-sdk/openai-compatible\"\n",
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"}))
	if err != nil {
		t.Fatal(err)
	}
	frames := mustExecute(t, inst, "opencode-go/emu-model", false)
	if !framesHaveText(frames, "hi") {
		t.Fatalf("frames=%v", frames)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bearers) != 2 || bearers[0] != "bad-one" || bearers[1] != "good-two" {
		t.Fatalf("bearers=%q", bearers)
	}
}
