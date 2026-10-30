package standardplugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestCompatibleCredential_rejectsLiteralSecrets(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	kinds := []string{
		CustomOpenAILegacyCompatibleID,
		CustomOpenAIResponsesCompatibleID,
		CustomAnthropicCompatibleID,
	}
	forbidden := []struct {
		name string
		raw  string
	}{
		{name: "api_key", raw: "backend_prefix: p\nbase_url: http://127.0.0.1:9/v1\napi_key: sk-literal-secret\n"},
		{name: "api_keys", raw: "backend_prefix: p\nbase_url: http://127.0.0.1:9/v1\napi_keys: [sk-literal-list]\n"},
		{name: "credentials", raw: "backend_prefix: p\nbase_url: http://127.0.0.1:9/v1\ncredentials:\n  - id: c1\n    api_key: sk-cred-secret\n"},
	}
	for _, kind := range kinds {
		for _, tc := range forbidden {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				var node yaml.Node
				if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
					t.Fatal(err)
				}
				_, err := reg.BuildBackendWithLifecycle(kind, "secret-test", node, nil, pluginreg.BackendFactoryDeps{})
				if err == nil {
					t.Fatal("expected forbidden literal secret rejection")
				}
				msg := err.Error()
				if !strings.Contains(msg, "forbidden") || !strings.Contains(msg, tc.name) {
					t.Fatalf("error = %q, want forbidden %q", msg, tc.name)
				}
				for _, secret := range []string{"sk-literal-secret", "sk-literal-list", "sk-cred-secret"} {
					if strings.Contains(msg, secret) {
						t.Fatalf("error leaked secret material: %q", msg)
					}
				}
			})
		}
	}
}

func TestCompatibleCredential_envRootPoolsIndependentPerInstance(t *testing.T) {
	rootA := "COMPAT_CRED_POOL_A"
	rootB := "COMPAT_CRED_POOL_B"
	clearCustomEnvRoot(t, rootA)
	clearCustomEnvRoot(t, rootB)
	t.Setenv(rootA, "secret-a-primary")
	t.Setenv(rootA+"_2", "secret-a-secondary")
	t.Setenv(rootB, "secret-b-only")

	var authA, authB string
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authA = r.Header.Get("Authorization")
		if authA == "" {
			authA = r.Header.Get("x-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-a","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"}]}`)
	}))
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authB = r.Header.Get("Authorization")
		if authB == "" {
			authB = r.Header.Get("x-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-b","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srvA.Close)
	t.Cleanup(srvB.Close)

	reg := customCompatibleRegistry(t)
	beA := mustBuildCompatible(t, reg, CustomOpenAILegacyCompatibleID, "pool-a", fmt.Sprintf(`
backend_prefix: pool-a
base_url: %s/v1
api_key_env_var_root: %s
`, srvA.URL, rootA), srvA.Client())
	beB := mustBuildCompatible(t, reg, CustomOpenAILegacyCompatibleID, "pool-b", fmt.Sprintf(`
backend_prefix: pool-b
base_url: %s/v1
api_key_env_var_root: %s
`, srvB.URL, rootB), srvB.Client())

	call := customCompatibleTestCall(aipapi.OperationOpenAIChatCompletions)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	openDrainCompatible(t, beA, call, cand)
	openDrainCompatible(t, beB, call, cand)

	if !strings.Contains(authA, "secret-a-primary") {
		t.Fatalf("instance A auth=%q want env root A primary", authA)
	}
	if strings.Contains(authA, "secret-b-only") {
		t.Fatalf("instance A leaked B credential: %q", authA)
	}
	if !strings.Contains(authB, "secret-b-only") {
		t.Fatalf("instance B auth=%q want env root B", authB)
	}
	if strings.Contains(authB, "secret-a-primary") || strings.Contains(authB, "secret-a-secondary") {
		t.Fatalf("instance B leaked A credential: %q", authB)
	}
}

func TestCompatibleNoAuth_omitsCredentialHeadersOpenAIAndAnthropic(t *testing.T) {
	t.Parallel()

	t.Run("openai_legacy", func(t *testing.T) {
		t.Parallel()
		var auth, xAPIKey string
		var modelsAuth, modelsXAPIKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/models") {
				modelsAuth = r.Header.Get("Authorization")
				modelsXAPIKey = r.Header.Get("x-api-key")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
				return
			}
			auth = r.Header.Get("Authorization")
			xAPIKey = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		t.Cleanup(srv.Close)

		reg := customCompatibleRegistry(t)
		be := mustBuildCompatible(t, reg, CustomOpenAILegacyCompatibleID, "noauth-openai", fmt.Sprintf(`
backend_prefix: noauth-openai
base_url: %s/v1
`, srv.URL), srv.Client())

		if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
			t.Fatalf("inventory: %v", err)
		}
		if modelsAuth != "" || modelsXAPIKey != "" {
			t.Fatalf("inventory sent credential headers Authorization=%q x-api-key=%q", modelsAuth, modelsXAPIKey)
		}

		openDrainCompatible(t, be, customCompatibleTestCall(aipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{Primary: routing.Primary{Model: "local-model"}})
		if auth != "" || xAPIKey != "" {
			t.Fatalf("execution sent credential headers Authorization=%q x-api-key=%q", auth, xAPIKey)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		t.Parallel()
		var auth, xAPIKey string
		var modelsAuth, modelsXAPIKey string
		const sse = "event: message_start\ndata: " +
			`{"type":"message_start","message":{"id":"m_stream","type":"message","role":"assistant","model":"claude-local","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` +
			"\n\n" +
			"event: content_block_start\ndata: " +
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` +
			"\n\n" +
			"event: content_block_delta\ndata: " +
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` +
			"\n\n" +
			"event: content_block_stop\ndata: " +
			`{"type":"content_block_stop","index":0}` +
			"\n\n" +
			"event: message_delta\ndata: " +
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}` +
			"\n\n" +
			"event: message_stop\ndata: " +
			`{"type":"message_stop"}` +
			"\n\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/models") {
				modelsAuth = r.Header.Get("Authorization")
				modelsXAPIKey = r.Header.Get("x-api-key")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-local","display_name":"Local"}]}`)
				return
			}
			auth = r.Header.Get("Authorization")
			xAPIKey = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, sse)
		}))
		t.Cleanup(srv.Close)

		reg := customCompatibleRegistry(t)
		be := mustBuildCompatible(t, reg, CustomAnthropicCompatibleID, "noauth-anthropic", fmt.Sprintf(`
backend_prefix: noauth-anthropic
base_url: %s
`, srv.URL), srv.Client())

		if _, err := be.ModelInventory.LoadModels(context.Background()); err != nil {
			t.Fatalf("inventory: %v", err)
		}
		if modelsAuth != "" || modelsXAPIKey != "" {
			t.Fatalf("inventory sent credential headers Authorization=%q x-api-key=%q", modelsAuth, modelsXAPIKey)
		}

		call := aipapi.Call{
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
			}},
		}
		openDrainCompatible(t, be, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-local"}})
		if auth != "" || xAPIKey != "" {
			t.Fatalf("execution sent credential headers Authorization=%q x-api-key=%q", auth, xAPIKey)
		}
	})
}

func TestCompatibleSecret_factoryErrorsNeverEchoLiteralValues(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := "backend_prefix: leakcheck\nbase_url: http://127.0.0.1:9/v1\napi_key: super-secret-value-should-not-leak\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackendWithLifecycle(CustomOpenAILegacyCompatibleID, "leakcheck", node, nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret-value-should-not-leak") {
		t.Fatalf("error echoed literal secret: %v", err)
	}
}

func mustBuildCompatible(t *testing.T, reg *pluginreg.Registry, kind, instanceID, raw string, client *http.Client) execbackend.Backend {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return buildCompatibleBackend(t, reg, kind, instanceID, node, client)
}

func openDrainCompatible(t *testing.T, be execbackend.Backend, call aipapi.Call, cand routing.AttemptCandidate) {
	t.Helper()
	if be.Open == nil {
		t.Fatal("backend Open is nil")
	}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = es.Close() }()
	for {
		_, rerr := es.Recv(context.Background())
		if rerr != nil {
			if rerr == io.EOF {
				return
			}
			t.Fatalf("Recv: %v", rerr)
		}
	}
}
