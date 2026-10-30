package standardplugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	refanthropic "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refgemini "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
	refchat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refresponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// TestBuiltinBackends_rejectLiteralStaticCredentials proves requirement 2.1
// for the built-in API-key families: literal secret-bearing YAML/config keys,
// including legacy singular, list, structured, interpolated and nested
// variants, are rejected before opaque plugin YAML delivery. Errors report the
// backend field path plus the approved environment root only, never values.
func TestBuiltinBackends_rejectLiteralStaticCredentials(t *testing.T) {
	t.Parallel()

	factories := []struct {
		name    string
		envRoot string
		build   func(node yaml.Node, keys UpstreamAPIKeys) error
	}{
		{
			name:    "openai-responses",
			envRoot: openAICredentialRoot,
			build: func(node yaml.Node, keys UpstreamAPIKeys) error {
				_, err := backendOpenAIResponses(node, nil, keys, identity.Config{})
				return err
			},
		},
		{
			name:    "openai-legacy",
			envRoot: openAICredentialRoot,
			build: func(node yaml.Node, keys UpstreamAPIKeys) error {
				_, err := backendOpenAILegacy(node, nil, keys, identity.Config{})
				return err
			},
		},
		{
			name:    "anthropic",
			envRoot: anthropicCredentialRoot,
			build: func(node yaml.Node, keys UpstreamAPIKeys) error {
				_, err := backendAnthropic(node, nil, keys, identity.Config{})
				return err
			},
		},
		{
			name:    "gemini",
			envRoot: geminiCredentialRoot,
			build: func(node yaml.Node, keys UpstreamAPIKeys) error {
				_, err := backendGemini(node, nil, keys, identity.Config{})
				return err
			},
		},
		{
			name:    "alibaba-token-plan-intl",
			envRoot: alibabaTokenPlanCredentialRoot,
			build: func(node yaml.Node, keys UpstreamAPIKeys) error {
				_, err := backendAlibabaTokenPlanIntl(node, nil, keys, identity.Config{})
				return err
			},
		},
	}

	variants := []struct {
		name string
		yaml string
		path string
	}{
		{name: "singular", yaml: "api_key: sk-live-secret\n", path: "config.api_key"},
		{name: "list", yaml: "api_keys:\n  - sk-live-list\n", path: "config.api_keys"},
		{name: "structured", yaml: "credentials:\n  - id: primary\n    api_key: sk-live-cred\n", path: "config.credentials"},
		{name: "nested_token", yaml: "credentials:\n  - id: primary\n    access_token: tok-live-nested\n", path: "config.credentials"},
		{name: "access_token", yaml: "access_token: tok-live-value\n", path: "config.access_token"},
		{name: "api_token", yaml: "api_token: tok-live-value\n", path: "config.api_token"},
		{name: "personal_access_token", yaml: "personal_access_token: pat-live-value\n", path: "config.personal_access_token"},
		{name: "pat", yaml: "pat: pat-live-value\n", path: "config.pat"},
		{name: "private_key", yaml: "private_key: -----BEGIN PRIVATE KEY-----\n", path: "config.private_key"},
		{name: "client_secret", yaml: "client_secret: shh-live-value\n", path: "config.client_secret"},
		{name: "interpolated", yaml: "api_key: ${OPENAI_API_KEY}\n", path: "config.api_key"},
		{name: "file_scheme", yaml: "api_key: file:/run/secrets/openai\n", path: "config.api_key"},
		{name: "vault_scheme", yaml: "api_keys:\n  - vault:secret/openai\n", path: "config.api_keys"},
		{name: "dotenv_scheme", yaml: "api_key: dotenv:.env:OPENAI_API_KEY\n", path: "config.api_key"},
		{name: "nested_models", yaml: "models:\n  source: inline\n  api_key: sk-nested-models\n", path: "config.models.api_key"},
		{name: "auth_token", yaml: "auth_token: tok-live-value\n", path: "config.auth_token"},
		{name: "bearer_token", yaml: "bearer_token: tok-live-value\n", path: "config.bearer_token"},
		{name: "credential", yaml: "credential: sk-live-cred\n", path: "config.credential"},
		{name: "env_scheme", yaml: "tokenizer: env:PROVIDER_TOKENIZER\n", path: "config.tokenizer"},
		{name: "deep_nesting", yaml: "outer:\n  middle:\n    inner:\n      api_key: sk-live-deep\n", path: "config.outer.middle.inner.api_key"},
		{name: "merge_alias", yaml: "shared: &shared\n  api_key: sk-live-merged\nextra:\n  <<: *shared\n", path: "config.shared.api_key"},
	}

	secrets := []string{
		"sk-live-secret", "sk-live-list", "sk-live-cred", "tok-live-value",
		"tok-live-nested", "pat-live-value", "shh-live-value", "sk-nested-models",
		"sk-live-deep", "sk-live-merged", "PROVIDER_TOKENIZER",
	}

	for _, f := range factories {
		for _, v := range variants {
			t.Run(f.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()
				raw := "base_url: https://api.example.test/v1\n" + v.yaml
				var node yaml.Node
				if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
					t.Fatal(err)
				}
				err := f.build(node, UpstreamAPIKeys{})
				if err == nil {
					t.Fatalf("%s: expected literal static-credential rejection for %s", f.name, v.name)
				}
				msg := err.Error()
				if !strings.Contains(msg, "forbidden") {
					t.Fatalf("%s/%s: error = %q, want forbidden marker", f.name, v.name, msg)
				}
				if !strings.Contains(msg, v.path) {
					t.Fatalf("%s/%s: error = %q, want field path %q", f.name, v.name, msg, v.path)
				}
				if !strings.Contains(msg, f.envRoot) {
					t.Fatalf("%s/%s: error = %q, want approved env root %q", f.name, v.name, msg, f.envRoot)
				}
				for _, secret := range secrets {
					if strings.Contains(msg, secret) {
						t.Fatalf("%s/%s: error leaked secret material: %q", f.name, v.name, msg)
					}
				}
			})
		}
	}
}

// TestBuiltinBackends_envOnlyKeysReachUpstream proves the preserved half of the
// contract (requirements 2.4/4.1): with literal-free YAML, host-resolved keys
// bound at registration reach the upstream request of every built-in API-key
// family, each from its own credential namespace. Reference emulators observe
// the raw credential the adapter actually sent, so a constructor that drops the
// binding or reads another family's field fails here.
func TestBuiltinBackends_envOnlyKeysReachUpstream(t *testing.T) {
	t.Parallel()

	keys := UpstreamAPIKeys{
		OpenAI:           []string{"env-openai"},
		Anthropic:        []string{"env-anthropic"},
		AlibabaTokenPlan: []string{"env-alibaba"},
		Gemini:           []string{"env-gemini"},
	}

	families := []struct {
		name    string
		baseDiv string
		build   func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error)
		call    func() aipapi.Call
		model   string
		handler func(capture func(string)) http.Handler
		wantKey string
	}{
		{
			name:    "openai-responses",
			baseDiv: "/v1",
			build: func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
				return backendOpenAIResponses(node, client, keys, identity.Config{})
			},
			call: func() aipapi.Call {
				return aipapi.Call{
					Invocation: aipapi.Invocation{
						Operation:     aipapi.OperationOpenAIResponses,
						DeliveryMode:  aipapi.DeliveryModeNonStreaming,
						TransportMode: aipapi.TransportModeNonStreaming,
					},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}},
				}
			},
			model: "gpt-4o-mini",
			handler: func(capture func(string)) http.Handler {
				return refresponses.NewHandler(refresponses.Config{OnAuthorizedCredential: capture})
			},
			wantKey: "env-openai",
		},
		{
			name:    "openai-legacy",
			baseDiv: "/v1",
			build: func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
				return backendOpenAILegacy(node, client, keys, identity.Config{})
			},
			call: func() aipapi.Call {
				return aipapi.Call{
					Invocation: aipapi.Invocation{
						Operation:     aipapi.OperationOpenAIChatCompletions,
						DeliveryMode:  aipapi.DeliveryModeNonStreaming,
						TransportMode: aipapi.TransportModeNonStreaming,
					},
					Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}},
				}
			},
			model: "gpt-4o-mini",
			handler: func(capture func(string)) http.Handler {
				return refchat.NewHandler(refchat.Config{OnAuthorizedCredential: capture})
			},
			wantKey: "env-openai",
		},
		{
			name: "anthropic",
			build: func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
				return backendAnthropic(node, client, keys, identity.Config{})
			},
			call: func() aipapi.Call {
				return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}}}
			},
			model: "claude-3-5-haiku-20241022",
			handler: func(capture func(string)) http.Handler {
				return refanthropic.NewHandler(refanthropic.Config{OnAuthorizedCredential: capture})
			},
			wantKey: "env-anthropic",
		},
		{
			name: "gemini",
			build: func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
				return backendGemini(node, client, keys, identity.Config{})
			},
			call: func() aipapi.Call {
				return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}}}
			},
			model: "gemini-2.0-flash",
			handler: func(capture func(string)) http.Handler {
				return refgemini.NewHandler(refgemini.Config{OnAuthorizedCredential: capture})
			},
			wantKey: "env-gemini",
		},
		{
			name: "alibaba-token-plan-intl",
			build: func(node yaml.Node, client *http.Client, keys UpstreamAPIKeys) (execbackend.Backend, error) {
				return backendAlibabaTokenPlanIntl(node, client, keys, identity.Config{})
			},
			call: func() aipapi.Call {
				return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}}}
			},
			model: "qwen3.7-plus",
			handler: func(capture func(string)) http.Handler {
				return refanthropic.NewHandler(refanthropic.Config{OnAuthorizedCredential: capture})
			},
			wantKey: "env-alibaba",
		},
	}

	for _, f := range families {
		t.Run(f.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var seen []string
			capture := func(key string) {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, key)
			}
			srv := httptest.NewServer(f.handler(capture))
			t.Cleanup(srv.Close)

			var node yaml.Node
			if err := yaml.Unmarshal([]byte("base_url: "+srv.URL+f.baseDiv+"\n"), &node); err != nil {
				t.Fatal(err)
			}
			be, err := f.build(node, srv.Client(), keys)
			if err != nil {
				t.Fatalf("build with env-only keys: %v", err)
			}
			stream, err := be.Open(t.Context(), f.call(), routing.AttemptCandidate{Primary: routing.Primary{Backend: f.name, Model: f.model}})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := aipapi.Collect(t.Context(), stream); err != nil {
				t.Fatalf("Collect: %v", err)
			}
			mu.Lock()
			got := append([]string(nil), seen...)
			mu.Unlock()
			if len(got) != 1 || got[0] != f.wantKey {
				t.Fatalf("upstream credential = %v, want exactly [%s] from the %s namespace", got, f.wantKey, f.name)
			}
		})
	}
}
